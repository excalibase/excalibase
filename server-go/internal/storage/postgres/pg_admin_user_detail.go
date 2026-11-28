package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/excalibase/provisioning-poc/internal/adminconsole"
)

const (
	userHistoryLimit = 20
	userAuditLimit   = 10
)

const userProfileSQL = `
	SELECT u.id, u.email, u.username, u.role, ` + statusExpr + `, u.email_verified_at, u.created_at, u.last_login_at, u.last_login_ip,
	       s.user_id IS NOT NULL, COALESCE(s.reason, ''), COALESCE(s.reason_code, ''), s.suspended_at
	FROM users u
	LEFT JOIN abuse_suspensions s ON s.user_id = u.id
	WHERE u.id = $1`

// userOrgsSQL is the orgs the account belongs to or owns; an owner without a
// membership row still shows, as owner.
const userOrgsSQL = `
	SELECT o.id, o.name, o.slug, o.tier, COALESCE(m.role, 'owner'), o.owner_id = $1
	FROM orgs o
	LEFT JOIN org_members m ON m.org_id = o.id AND m.user_id = $1
	WHERE m.user_id IS NOT NULL OR o.owner_id = $1
	ORDER BY o.name, o.id
	LIMIT ` + "100"

const userProjectsSQL = `
	SELECT di.project_id, COALESCE(di.project_name, ''), di.org_id, o.name, di.status
	FROM database_instances di
	JOIN orgs o ON o.id = di.org_id
	LEFT JOIN org_members m ON m.org_id = o.id AND m.user_id = $1
	WHERE m.user_id IS NOT NULL OR o.owner_id = $1
	ORDER BY o.name, di.project_id
	LIMIT ` + "100"

const suspensionHistorySQL = `
	SELECT a.action, COALESCE(a.user_id, ''), COALESCE(au.email, ''), COALESCE(a.details, ''), a.timestamp
	FROM audit_log a
	LEFT JOIN users au ON au.id = a.user_id
	WHERE a.via = '' AND a.resource = 'user' AND a.resource_id = $1 AND a.action IN ($2, $3)
	ORDER BY a.id DESC
	LIMIT $4`

const signInSQL = `SELECT provider, email, created_at FROM studio_identities WHERE user_id = $1 ORDER BY created_at, provider`

// ReadUserDetail reads one account's page. It selects no password hash, token or secret.
func (a *AdminConsole) ReadUserDetail(ctx context.Context, userID string) (*adminconsole.UserDetail, error) {
	detail := &adminconsole.UserDetail{}
	if err := a.readProfile(ctx, userID, detail); err != nil {
		return nil, err
	}
	steps := []func(context.Context, string, *adminconsole.UserDetail) error{
		a.readTrust, a.readOrgs, a.readProjects, a.readSuspensions, a.readSignIn, a.readRecentAudit,
	}
	for _, step := range steps {
		if err := step(ctx, userID, detail); err != nil {
			return nil, err
		}
	}
	return detail, nil
}

func (a *AdminConsole) readProfile(ctx context.Context, userID string, detail *adminconsole.UserDetail) error {
	var verified, signedUp, lastLogin, suspendedAt sql.NullTime
	var lastIP sql.NullString
	profile := &detail.Profile
	err := a.db.QueryRowContext(ctx, userProfileSQL, userID).Scan(
		&profile.ID, &profile.Email, &profile.Username, &profile.Role, &profile.Status, &verified, &signedUp, &lastLogin, &lastIP,
		&detail.Suspension.Suspended, &detail.Suspension.Reason, &detail.Suspension.ReasonCode, &suspendedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return adminconsole.ErrUserNotFound
	}
	if err != nil {
		return fmt.Errorf("read profile: %w", err)
	}
	profile.EmailVerified, profile.EmailVerifiedAt = verified.Valid, timePtr(verified)
	profile.SignedUpAt, profile.LastLoginAt, profile.LastLoginIP = timePtr(signedUp), timePtr(lastLogin), lastIP.String
	detail.Suspension.SuspendedAt = timePtr(suspendedAt)
	return nil
}

func (a *AdminConsole) readOrgs(ctx context.Context, userID string, detail *adminconsole.UserDetail) error {
	rows, err := a.db.QueryContext(ctx, userOrgsSQL, userID)
	if err != nil {
		return fmt.Errorf("read orgs: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var org adminconsole.UserOrg
		if err := rows.Scan(&org.ID, &org.Name, &org.Slug, &org.Tier, &org.Role, &org.Owner); err != nil {
			return fmt.Errorf("scan org: %w", err)
		}
		detail.Orgs = append(detail.Orgs, org)
	}
	return rows.Err()
}

func (a *AdminConsole) readProjects(ctx context.Context, userID string, detail *adminconsole.UserDetail) error {
	rows, err := a.db.QueryContext(ctx, userProjectsSQL, userID)
	if err != nil {
		return fmt.Errorf("read projects: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var project adminconsole.UserProject
		if err := rows.Scan(&project.ProjectID, &project.Name, &project.OrgID, &project.OrgName, &project.Status); err != nil {
			return fmt.Errorf("scan project: %w", err)
		}
		detail.Projects = append(detail.Projects, project)
	}
	return rows.Err()
}

func (a *AdminConsole) readSuspensions(ctx context.Context, userID string, detail *adminconsole.UserDetail) error {
	rows, err := a.db.QueryContext(ctx, suspensionHistorySQL, userID, adminconsole.AuditActionSuspend, adminconsole.AuditActionUnsuspend, userHistoryLimit)
	if err != nil {
		return fmt.Errorf("read suspension history: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var event adminconsole.SuspensionEvent
		var at sql.NullTime
		if err := rows.Scan(&event.Action, &event.ActorID, &event.ActorEmail, &event.Reason, &at); err != nil {
			return fmt.Errorf("scan suspension event: %w", err)
		}
		event.At = timePtr(at)
		detail.Suspensions = append(detail.Suspensions, event)
	}
	return rows.Err()
}

// readSignIn lists the linked providers. The platform keeps no record of a
// usable password (a provider sign-up stores an unusable one), so an account
// with no linked provider is listed as signing in by password.
func (a *AdminConsole) readSignIn(ctx context.Context, userID string, detail *adminconsole.UserDetail) error {
	rows, err := a.db.QueryContext(ctx, signInSQL, userID)
	if err != nil {
		return fmt.Errorf("read sign-in methods: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		method := adminconsole.SignInMethod{Kind: "provider"}
		var linked sql.NullTime
		if err := rows.Scan(&method.Provider, &method.Email, &linked); err != nil {
			return fmt.Errorf("scan sign-in method: %w", err)
		}
		method.LinkedAt = timePtr(linked)
		detail.SignIn = append(detail.SignIn, method)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if len(detail.SignIn) == 0 {
		detail.SignIn = []adminconsole.SignInMethod{{Kind: "password"}}
	}
	return nil
}

func (a *AdminConsole) readRecentAudit(ctx context.Context, userID string, detail *adminconsole.UserDetail) error {
	rows, err := a.db.QueryContext(ctx, recentAuditForUserSQL, userID, userAuditLimit)
	if err != nil {
		return fmt.Errorf("read recent audit: %w", err)
	}
	defer rows.Close()
	detail.Audit, err = scanAuditRows(rows)
	return err
}
