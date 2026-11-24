package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/excalibase/provisioning-poc/internal/adminconsole"
)

func (a *AdminConsole) FindUser(ctx context.Context, id string) (*adminconsole.UserRef, error) {
	var user adminconsole.UserRef
	err := a.db.QueryRowContext(ctx, `
		SELECT u.id, u.email, u.role, u.active, s.user_id IS NOT NULL
		FROM users u LEFT JOIN abuse_suspensions s ON s.user_id = u.id
		WHERE u.id = $1 AND u.kind = 'human'`, id).Scan(&user.ID, &user.Email, &user.Role, &user.Active, &user.Suspended)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &user, nil
}

// OwnedApps lists the apps in projects of orgs the account owns.
func (a *AdminConsole) OwnedApps(ctx context.Context, userID string) ([]adminconsole.OwnedApp, error) {
	rows, err := a.db.QueryContext(ctx, `
		SELECT ap.project_id, ap.id, ap.name, ap.status, COALESCE((ap.doc->>'replicas')::int, 0)
		FROM apps ap
		JOIN database_instances di ON di.project_id = ap.project_id
		JOIN orgs o ON o.id = di.org_id
		WHERE o.owner_id = $1
		ORDER BY ap.project_id, ap.created_at, ap.id`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []adminconsole.OwnedApp{}
	for rows.Next() {
		var app adminconsole.OwnedApp
		if err := rows.Scan(&app.ProjectID, &app.AppID, &app.Name, &app.Status, &app.Replicas); err != nil {
			return nil, err
		}
		out = append(out, app)
	}
	return out, rows.Err()
}

// OwnedProjects lists the projects of the orgs the account owns. A project it
// merely created in someone else's org is not its to take down.
func (a *AdminConsole) OwnedProjects(ctx context.Context, userID string) ([]string, error) {
	rows, err := a.db.QueryContext(ctx, `
		SELECT di.project_id
		FROM database_instances di
		JOIN orgs o ON o.id = di.org_id
		WHERE o.owner_id = $1
		ORDER BY di.project_id`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// Suspend blocks the account, writes the reason and records the apps it is
// about to stop, all or nothing.
func (a *AdminConsole) Suspend(ctx context.Context, suspension adminconsole.Suspension, apps []adminconsole.SuspendedApp) error {
	return a.inTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `UPDATE users SET active = FALSE, updated_at = NOW() WHERE id = $1`, suspension.UserID); err != nil {
			return fmt.Errorf("deactivate: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO abuse_suspensions (user_id, email, reason, suspended_at) VALUES ($1, $2, $3, $4)
			ON CONFLICT (user_id) DO UPDATE SET email = EXCLUDED.email, reason = EXCLUDED.reason, suspended_at = EXCLUDED.suspended_at`,
			suspension.UserID, suspension.Email, suspension.Reason, suspension.At); err != nil {
			return fmt.Errorf("record suspension: %w", err)
		}
		return recordSuspendedApps(ctx, tx, suspension.UserID, apps)
	})
}

// RecordSuspendedApps keeps the apps a suspension is about to stop, with their replicas.
func (a *AdminConsole) RecordSuspendedApps(ctx context.Context, userID string, apps []adminconsole.SuspendedApp) error {
	return a.inTx(ctx, func(tx *sql.Tx) error {
		return recordSuspendedApps(ctx, tx, userID, apps)
	})
}

func recordSuspendedApps(ctx context.Context, tx *sql.Tx, userID string, apps []adminconsole.SuspendedApp) error {
	for _, app := range apps {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO abuse_suspension_apps (user_id, project_id, app_id, app_name, previous_replicas)
			VALUES ($1, $2, $3, $4, $5)
			ON CONFLICT (user_id, project_id, app_id) DO UPDATE SET previous_replicas = EXCLUDED.previous_replicas, app_name = EXCLUDED.app_name`,
			userID, app.ProjectID, app.AppID, app.AppName, app.PreviousReplicas); err != nil {
			return fmt.Errorf("record app: %w", err)
		}
	}
	return nil
}

// Unsuspend reactivates the account and removes the suspension. The app
// records stay until each app is resumed.
func (a *AdminConsole) Unsuspend(ctx context.Context, userID string) error {
	return a.inTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `UPDATE users SET active = TRUE, updated_at = NOW() WHERE id = $1`, userID); err != nil {
			return fmt.Errorf("reactivate: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM abuse_suspensions WHERE user_id = $1`, userID); err != nil {
			return fmt.Errorf("remove suspension: %w", err)
		}
		return nil
	})
}

func (a *AdminConsole) SuspendedApps(ctx context.Context, userID string) ([]adminconsole.SuspendedApp, error) {
	rows, err := a.db.QueryContext(ctx, `
		SELECT project_id, app_id, app_name, previous_replicas FROM abuse_suspension_apps
		WHERE user_id = $1 ORDER BY project_id, app_id`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []adminconsole.SuspendedApp{}
	for rows.Next() {
		var app adminconsole.SuspendedApp
		if err := rows.Scan(&app.ProjectID, &app.AppID, &app.AppName, &app.PreviousReplicas); err != nil {
			return nil, err
		}
		out = append(out, app)
	}
	return out, rows.Err()
}

func (a *AdminConsole) ForgetSuspendedApp(ctx context.Context, userID, projectID, appID string) error {
	_, err := a.db.ExecContext(ctx, `DELETE FROM abuse_suspension_apps WHERE user_id = $1 AND project_id = $2 AND app_id = $3`, userID, projectID, appID)
	return err
}

// OwnerSuspendedOrgs returns the ids of orgs whose owner is suspended, so a
// project list can say "Suspended" instead of the database's own status.
func (a *AdminConsole) OwnerSuspendedOrgs(ctx context.Context) (map[string]bool, error) {
	rows, err := a.db.QueryContext(ctx, `SELECT o.id FROM orgs o JOIN abuse_suspensions s ON s.user_id = o.owner_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out[id] = true
	}
	return out, rows.Err()
}

// SuspendedUsers returns the ids of every abuse-suspended account.
func (a *AdminConsole) SuspendedUsers(ctx context.Context) (map[string]bool, error) {
	rows, err := a.db.QueryContext(ctx, `SELECT user_id FROM abuse_suspensions`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out[id] = true
	}
	return out, rows.Err()
}

func (a *AdminConsole) inTx(ctx context.Context, work func(tx *sql.Tx) error) error {
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := work(tx); err != nil {
		if rbErr := tx.Rollback(); rbErr != nil {
			return errors.Join(err, rbErr)
		}
		return err
	}
	return tx.Commit()
}

// RecordUserLogin stores when and from where the account last signed in, for
// the admin's users table.
func (s *Store) RecordUserLogin(ctx context.Context, userID, ip string, at time.Time) error {
	_, err := s.db.ExecContext(ctx, `UPDATE users SET last_login_at = $2, last_login_ip = $3 WHERE id = $1`, userID, at.UTC(), ip)
	return err
}
