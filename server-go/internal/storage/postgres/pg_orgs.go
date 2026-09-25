package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/storage"
)

func (s *Store) CreateOrg(ctx context.Context, org *domain.Org) error {
	now := time.Now().UTC()
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO orgs (id, name, slug, tier, owner_id, created_at, updated_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		org.ID, org.Name, org.Slug, org.Tier, org.OwnerID, now, now)
	return err
}

func (s *Store) FindOrgByID(ctx context.Context, id string) (*domain.Org, error) {
	return s.scanOrg(s.db.QueryRowContext(ctx,
		`SELECT id, name, slug, tier, owner_id, created_at, updated_at FROM orgs WHERE id = $1`, id))
}

func (s *Store) FindOrgBySlug(ctx context.Context, slug string) (*domain.Org, error) {
	return s.scanOrg(s.db.QueryRowContext(ctx,
		`SELECT id, name, slug, tier, owner_id, created_at, updated_at FROM orgs WHERE slug = $1`, slug))
}

func (s *Store) FindOrgsByUser(ctx context.Context, userID string) ([]*domain.Org, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT o.id, o.name, o.slug, o.tier, o.owner_id, o.created_at, o.updated_at
		 FROM orgs o JOIN org_members m ON o.id = m.org_id
		 WHERE m.user_id = $1`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var orgs []*domain.Org
	for rows.Next() {
		org, err := s.scanOrgRow(rows)
		if err != nil {
			return nil, err
		}
		orgs = append(orgs, org)
	}
	return orgs, nil
}

func (s *Store) FindAllOrgs(ctx context.Context) ([]*domain.Org, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, name, slug, tier, owner_id, created_at, updated_at FROM orgs`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var orgs []*domain.Org
	for rows.Next() {
		org, err := s.scanOrgRow(rows)
		if err != nil {
			return nil, err
		}
		orgs = append(orgs, org)
	}
	return orgs, nil
}

func (s *Store) UpdateOrg(ctx context.Context, org *domain.Org) error {
	now := time.Now().UTC()
	_, err := s.db.ExecContext(ctx,
		`UPDATE orgs SET name = $1, slug = $2, tier = $3, updated_at = $4 WHERE id = $5`,
		org.Name, org.Slug, org.Tier, now, org.ID)
	return err
}

func (s *Store) DeleteOrg(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM orgs WHERE id = $1`, id)
	return err
}

// --- Org Members ---

func (s *Store) AddOrgMember(ctx context.Context, m *domain.OrgMember) error {
	now := time.Now().UTC()
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO org_members (org_id, user_id, role, created_at) VALUES ($1, $2, $3, $4)`,
		m.OrgID, m.UserID, m.Role, now)
	return err
}

func (s *Store) RemoveOrgMember(ctx context.Context, orgID, userID string) error {
	_, err := s.db.ExecContext(ctx,
		`DELETE FROM org_members WHERE org_id = $1 AND user_id = $2`, orgID, userID)
	return err
}

func (s *Store) UpdateOrgMemberRole(ctx context.Context, orgID, userID, role string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE org_members SET role = $1 WHERE org_id = $2 AND user_id = $3`, role, orgID, userID)
	return err
}

func (s *Store) ListOrgMembers(ctx context.Context, orgID string) ([]*domain.OrgMember, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT m.org_id, m.user_id, m.role, m.created_at, u.email, u.username
		 FROM org_members m JOIN users u ON m.user_id = u.id
		 WHERE m.org_id = $1`, orgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var members []*domain.OrgMember
	for rows.Next() {
		m := &domain.OrgMember{}
		var createdAt sql.NullTime
		if err := rows.Scan(&m.OrgID, &m.UserID, &m.Role, &createdAt, &m.Email, &m.Username); err != nil {
			return nil, err
		}
		if createdAt.Valid {
			m.CreatedAt = &domain.FlexTime{Time: createdAt.Time}
		}
		members = append(members, m)
	}
	return members, nil
}

func (s *Store) GetOrgMember(ctx context.Context, orgID, userID string) (*domain.OrgMember, error) {
	m := &domain.OrgMember{}
	var createdAt sql.NullTime
	err := s.db.QueryRowContext(ctx,
		`SELECT m.org_id, m.user_id, m.role, m.created_at, u.email, u.username
		 FROM org_members m JOIN users u ON m.user_id = u.id
		 WHERE m.org_id = $1 AND m.user_id = $2`, orgID, userID,
	).Scan(&m.OrgID, &m.UserID, &m.Role, &createdAt, &m.Email, &m.Username)
	if err != nil {
		return nil, fmt.Errorf("org member not found: %w", err)
	}
	if createdAt.Valid {
		m.CreatedAt = &domain.FlexTime{Time: createdAt.Time}
	}
	return m, nil
}

// --- Project Members ---

func (s *Store) AddProjectMember(ctx context.Context, m *domain.ProjectMember) error {
	now := time.Now().UTC()
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO project_members (project_id, org_id, user_id, role, created_at) VALUES ($1, $2, $3, $4, $5)`,
		m.ProjectID, m.OrgID, m.UserID, m.Role, now)
	return err
}

func (s *Store) RemoveProjectMember(ctx context.Context, projectID, userID string) error {
	_, err := s.db.ExecContext(ctx,
		`DELETE FROM project_members WHERE project_id = $1 AND user_id = $2`, projectID, userID)
	return err
}

func (s *Store) UpdateProjectMemberRole(ctx context.Context, projectID, userID, role string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE project_members SET role = $1 WHERE project_id = $2 AND user_id = $3`, role, projectID, userID)
	return err
}

func (s *Store) ListProjectMembers(ctx context.Context, projectID string) ([]*domain.ProjectMember, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT m.project_id, m.org_id, m.user_id, m.role, m.created_at, u.email, u.username
		 FROM project_members m JOIN users u ON m.user_id = u.id
		 WHERE m.project_id = $1`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var members []*domain.ProjectMember
	for rows.Next() {
		m := &domain.ProjectMember{}
		var createdAt sql.NullTime
		if err := rows.Scan(&m.ProjectID, &m.OrgID, &m.UserID, &m.Role, &createdAt, &m.Email, &m.Username); err != nil {
			return nil, err
		}
		if createdAt.Valid {
			m.CreatedAt = &domain.FlexTime{Time: createdAt.Time}
		}
		members = append(members, m)
	}
	return members, nil
}

func (s *Store) GetProjectMember(ctx context.Context, projectID, userID string) (*domain.ProjectMember, error) {
	m := &domain.ProjectMember{}
	var createdAt sql.NullTime
	err := s.db.QueryRowContext(ctx,
		`SELECT m.project_id, m.org_id, m.user_id, m.role, m.created_at, u.email, u.username
		 FROM project_members m JOIN users u ON m.user_id = u.id
		 WHERE m.project_id = $1 AND m.user_id = $2`, projectID, userID,
	).Scan(&m.ProjectID, &m.OrgID, &m.UserID, &m.Role, &createdAt, &m.Email, &m.Username)
	if err != nil {
		return nil, fmt.Errorf("project member not found: %w", err)
	}
	if createdAt.Valid {
		m.CreatedAt = &domain.FlexTime{Time: createdAt.Time}
	}
	return m, nil
}

// --- Pending Invites ---

// CreatePendingInvite files the invite, replacing any earlier one for the same
// address in the org: re-inviting issues a new link and the old one stops working.
func (s *Store) CreatePendingInvite(ctx context.Context, invite *domain.PendingInvite) error {
	if invite.TokenHash == "" || invite.ExpiresAt == nil {
		return errors.New("pending invite needs a token hash and an expiry")
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO pending_invites (org_id, email, role, invited_by, token_hash, expires_at)
		 VALUES ($1, $2, $3, $4, $5, $6)
		 ON CONFLICT (org_id, email) DO UPDATE SET
		   role = EXCLUDED.role, invited_by = EXCLUDED.invited_by,
		   token_hash = EXCLUDED.token_hash, expires_at = EXCLUDED.expires_at,
		   created_at = NOW()`,
		invite.OrgID, invite.Email, invite.Role, invite.InvitedBy, invite.TokenHash, invite.ExpiresAt.Time)
	return err
}

const pendingInviteColumns = `id, org_id, email, role, invited_by, expires_at, created_at`

func scanPendingInvite(row rowScanner) (*domain.PendingInvite, error) {
	inv := &domain.PendingInvite{}
	var expiresAt, createdAt sql.NullTime
	if err := row.Scan(&inv.ID, &inv.OrgID, &inv.Email, &inv.Role, &inv.InvitedBy, &expiresAt, &createdAt); err != nil {
		return nil, err
	}
	if expiresAt.Valid {
		inv.ExpiresAt = &domain.FlexTime{Time: expiresAt.Time}
	}
	if createdAt.Valid {
		inv.CreatedAt = &domain.FlexTime{Time: createdAt.Time}
	}
	return inv, nil
}

func (s *Store) FindPendingInviteByToken(ctx context.Context, tokenHash string, now time.Time) (*domain.PendingInvite, error) {
	inv, err := scanPendingInvite(s.db.QueryRowContext(ctx,
		`SELECT `+pendingInviteColumns+` FROM pending_invites WHERE token_hash = $1 AND expires_at > $2`,
		tokenHash, now))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, storage.ErrInviteInvalid
	}
	return inv, err
}

func (s *Store) AcceptPendingInvite(ctx context.Context, tokenHash, userID string, now time.Time) (*domain.PendingInvite, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	inv, err := scanPendingInvite(tx.QueryRowContext(ctx,
		`DELETE FROM pending_invites WHERE token_hash = $1 AND expires_at > $2 RETURNING `+pendingInviteColumns,
		tokenHash, now))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, storage.ErrInviteInvalid
	}
	if err != nil {
		return nil, err
	}
	res, err := tx.ExecContext(ctx,
		`INSERT INTO org_members (org_id, user_id, role, created_at) VALUES ($1, $2, $3, $4)
		 ON CONFLICT DO NOTHING`,
		inv.OrgID, userID, inv.Role, now.UTC())
	if err != nil {
		return nil, err
	}
	if added, err := res.RowsAffected(); err != nil || added == 0 {
		return nil, errors.Join(storage.ErrAlreadyOrgMember, err)
	}
	return inv, tx.Commit()
}

func (s *Store) DeletePendingInvite(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM pending_invites WHERE id = $1`, id)
	return err
}

func (s *Store) ListPendingInvites(ctx context.Context, orgID string) ([]*domain.PendingInvite, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+pendingInviteColumns+` FROM pending_invites WHERE org_id = $1`, orgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var invites []*domain.PendingInvite
	for rows.Next() {
		inv, err := scanPendingInvite(rows)
		if err != nil {
			return nil, err
		}
		invites = append(invites, inv)
	}
	return invites, rows.Err()
}

// --- Scan helpers ---

func (s *Store) scanOrg(row *sql.Row) (*domain.Org, error) {
	org := &domain.Org{}
	var createdAt, updatedAt sql.NullTime
	err := row.Scan(&org.ID, &org.Name, &org.Slug, &org.Tier, &org.OwnerID, &createdAt, &updatedAt)
	if err != nil {
		return nil, fmt.Errorf("org not found: %w", err)
	}
	if createdAt.Valid {
		org.CreatedAt = &domain.FlexTime{Time: createdAt.Time}
	}
	if updatedAt.Valid {
		org.UpdatedAt = &domain.FlexTime{Time: updatedAt.Time}
	}
	return org, nil
}

func (s *Store) scanOrgRow(rows *sql.Rows) (*domain.Org, error) {
	org := &domain.Org{}
	var createdAt, updatedAt sql.NullTime
	err := rows.Scan(&org.ID, &org.Name, &org.Slug, &org.Tier, &org.OwnerID, &createdAt, &updatedAt)
	if err != nil {
		return nil, err
	}
	if createdAt.Valid {
		org.CreatedAt = &domain.FlexTime{Time: createdAt.Time}
	}
	if updatedAt.Valid {
		org.UpdatedAt = &domain.FlexTime{Time: updatedAt.Time}
	}
	return org, nil
}
