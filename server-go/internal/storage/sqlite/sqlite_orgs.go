package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
)

func (s *Store) CreateOrg(ctx context.Context, org *domain.Org) error {
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO orgs (id, name, slug, tier, owner_id, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		org.ID, org.Name, org.Slug, org.Tier, org.OwnerID, now, now)
	return err
}

func (s *Store) FindOrgByID(ctx context.Context, id string) (*domain.Org, error) {
	return s.scanOrg(s.db.QueryRowContext(ctx,
		`SELECT id, name, slug, tier, owner_id, created_at, updated_at FROM orgs WHERE id = ?`, id))
}

func (s *Store) FindOrgBySlug(ctx context.Context, slug string) (*domain.Org, error) {
	return s.scanOrg(s.db.QueryRowContext(ctx,
		`SELECT id, name, slug, tier, owner_id, created_at, updated_at FROM orgs WHERE slug = ?`, slug))
}

func (s *Store) FindOrgsByUser(ctx context.Context, userID string) ([]*domain.Org, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT o.id, o.name, o.slug, o.tier, o.owner_id, o.created_at, o.updated_at
		 FROM orgs o JOIN org_members m ON o.id = m.org_id
		 WHERE m.user_id = ?`, userID)
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
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := s.db.ExecContext(ctx,
		`UPDATE orgs SET name = ?, slug = ?, tier = ?, updated_at = ? WHERE id = ?`,
		org.Name, org.Slug, org.Tier, now, org.ID)
	return err
}

func (s *Store) DeleteOrg(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM orgs WHERE id = ?`, id)
	return err
}

// --- Org Members ---

func (s *Store) AddOrgMember(ctx context.Context, m *domain.OrgMember) error {
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO org_members (org_id, user_id, role, created_at) VALUES (?, ?, ?, ?)`,
		m.OrgID, m.UserID, m.Role, now)
	return err
}

func (s *Store) RemoveOrgMember(ctx context.Context, orgID, userID string) error {
	_, err := s.db.ExecContext(ctx,
		`DELETE FROM org_members WHERE org_id = ? AND user_id = ?`, orgID, userID)
	return err
}

func (s *Store) UpdateOrgMemberRole(ctx context.Context, orgID, userID, role string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE org_members SET role = ? WHERE org_id = ? AND user_id = ?`, role, orgID, userID)
	return err
}

func (s *Store) ListOrgMembers(ctx context.Context, orgID string) ([]*domain.OrgMember, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT m.org_id, m.user_id, m.role, m.created_at, u.email, u.username
		 FROM org_members m JOIN users u ON m.user_id = u.id
		 WHERE m.org_id = ?`, orgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var members []*domain.OrgMember
	for rows.Next() {
		m := &domain.OrgMember{}
		var createdAt sql.NullString
		if err := rows.Scan(&m.OrgID, &m.UserID, &m.Role, &createdAt, &m.Email, &m.Username); err != nil {
			return nil, err
		}
		if createdAt.Valid {
			t, _ := time.Parse(time.RFC3339, createdAt.String)
			m.CreatedAt = &domain.FlexTime{Time: t}
		}
		members = append(members, m)
	}
	return members, nil
}

func (s *Store) GetOrgMember(ctx context.Context, orgID, userID string) (*domain.OrgMember, error) {
	m := &domain.OrgMember{}
	var createdAt sql.NullString
	err := s.db.QueryRowContext(ctx,
		`SELECT m.org_id, m.user_id, m.role, m.created_at, u.email, u.username
		 FROM org_members m JOIN users u ON m.user_id = u.id
		 WHERE m.org_id = ? AND m.user_id = ?`, orgID, userID,
	).Scan(&m.OrgID, &m.UserID, &m.Role, &createdAt, &m.Email, &m.Username)
	if err != nil {
		return nil, fmt.Errorf("org member not found: %w", err)
	}
	if createdAt.Valid {
		t, _ := time.Parse(time.RFC3339, createdAt.String)
		m.CreatedAt = &domain.FlexTime{Time: t}
	}
	return m, nil
}

// --- Project Members ---

func (s *Store) AddProjectMember(ctx context.Context, m *domain.ProjectMember) error {
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO project_members (project_id, org_id, user_id, role, created_at) VALUES (?, ?, ?, ?, ?)`,
		m.ProjectID, m.OrgID, m.UserID, m.Role, now)
	return err
}

func (s *Store) RemoveProjectMember(ctx context.Context, projectID, userID string) error {
	_, err := s.db.ExecContext(ctx,
		`DELETE FROM project_members WHERE project_id = ? AND user_id = ?`, projectID, userID)
	return err
}

func (s *Store) UpdateProjectMemberRole(ctx context.Context, projectID, userID, role string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE project_members SET role = ? WHERE project_id = ? AND user_id = ?`, role, projectID, userID)
	return err
}

func (s *Store) ListProjectMembers(ctx context.Context, projectID string) ([]*domain.ProjectMember, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT m.project_id, m.org_id, m.user_id, m.role, m.created_at, u.email, u.username
		 FROM project_members m JOIN users u ON m.user_id = u.id
		 WHERE m.project_id = ?`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var members []*domain.ProjectMember
	for rows.Next() {
		m := &domain.ProjectMember{}
		var createdAt sql.NullString
		if err := rows.Scan(&m.ProjectID, &m.OrgID, &m.UserID, &m.Role, &createdAt, &m.Email, &m.Username); err != nil {
			return nil, err
		}
		if createdAt.Valid {
			t, _ := time.Parse(time.RFC3339, createdAt.String)
			m.CreatedAt = &domain.FlexTime{Time: t}
		}
		members = append(members, m)
	}
	return members, nil
}

func (s *Store) GetProjectMember(ctx context.Context, projectID, userID string) (*domain.ProjectMember, error) {
	m := &domain.ProjectMember{}
	var createdAt sql.NullString
	err := s.db.QueryRowContext(ctx,
		`SELECT m.project_id, m.org_id, m.user_id, m.role, m.created_at, u.email, u.username
		 FROM project_members m JOIN users u ON m.user_id = u.id
		 WHERE m.project_id = ? AND m.user_id = ?`, projectID, userID,
	).Scan(&m.ProjectID, &m.OrgID, &m.UserID, &m.Role, &createdAt, &m.Email, &m.Username)
	if err != nil {
		return nil, fmt.Errorf("project member not found: %w", err)
	}
	if createdAt.Valid {
		t, _ := time.Parse(time.RFC3339, createdAt.String)
		m.CreatedAt = &domain.FlexTime{Time: t}
	}
	return m, nil
}

// --- Pending Invites ---

func (s *Store) CreatePendingInvite(ctx context.Context, invite *domain.PendingInvite) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO pending_invites (org_id, email, role, invited_by) VALUES (?, ?, ?, ?)`,
		invite.OrgID, invite.Email, invite.Role, invite.InvitedBy)
	return err
}

func (s *Store) FindPendingInvitesByEmail(ctx context.Context, email string) ([]*domain.PendingInvite, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, org_id, email, role, invited_by, created_at FROM pending_invites WHERE email = ?`, email)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var invites []*domain.PendingInvite
	for rows.Next() {
		inv := &domain.PendingInvite{}
		var createdAt sql.NullString
		if err := rows.Scan(&inv.ID, &inv.OrgID, &inv.Email, &inv.Role, &inv.InvitedBy, &createdAt); err != nil {
			return nil, err
		}
		if createdAt.Valid {
			t, _ := time.Parse(time.RFC3339, createdAt.String)
			inv.CreatedAt = &domain.FlexTime{Time: t}
		}
		invites = append(invites, inv)
	}
	return invites, nil
}

func (s *Store) DeletePendingInvite(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM pending_invites WHERE id = ?`, id)
	return err
}

func (s *Store) ListPendingInvites(ctx context.Context, orgID string) ([]*domain.PendingInvite, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, org_id, email, role, invited_by, created_at FROM pending_invites WHERE org_id = ?`, orgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var invites []*domain.PendingInvite
	for rows.Next() {
		inv := &domain.PendingInvite{}
		var createdAt sql.NullString
		if err := rows.Scan(&inv.ID, &inv.OrgID, &inv.Email, &inv.Role, &inv.InvitedBy, &createdAt); err != nil {
			return nil, err
		}
		if createdAt.Valid {
			t, _ := time.Parse(time.RFC3339, createdAt.String)
			inv.CreatedAt = &domain.FlexTime{Time: t}
		}
		invites = append(invites, inv)
	}
	return invites, nil
}

// --- Scan helpers ---

func (s *Store) scanOrg(row *sql.Row) (*domain.Org, error) {
	org := &domain.Org{}
	var createdAt, updatedAt sql.NullString
	err := row.Scan(&org.ID, &org.Name, &org.Slug, &org.Tier, &org.OwnerID, &createdAt, &updatedAt)
	if err != nil {
		return nil, fmt.Errorf("org not found: %w", err)
	}
	if createdAt.Valid {
		t, _ := time.Parse(time.RFC3339, createdAt.String)
		org.CreatedAt = &domain.FlexTime{Time: t}
	}
	if updatedAt.Valid {
		t, _ := time.Parse(time.RFC3339, updatedAt.String)
		org.UpdatedAt = &domain.FlexTime{Time: t}
	}
	return org, nil
}

func (s *Store) scanOrgRow(rows *sql.Rows) (*domain.Org, error) {
	org := &domain.Org{}
	var createdAt, updatedAt sql.NullString
	err := rows.Scan(&org.ID, &org.Name, &org.Slug, &org.Tier, &org.OwnerID, &createdAt, &updatedAt)
	if err != nil {
		return nil, err
	}
	if createdAt.Valid {
		t, _ := time.Parse(time.RFC3339, createdAt.String)
		org.CreatedAt = &domain.FlexTime{Time: t}
	}
	if updatedAt.Valid {
		t, _ := time.Parse(time.RFC3339, updatedAt.String)
		org.UpdatedAt = &domain.FlexTime{Time: t}
	}
	return org, nil
}
