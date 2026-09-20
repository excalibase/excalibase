package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/storage"
	"github.com/lib/pq"
)

// ErrGrantNotFound is returned when a grant lookup or delete matches no row.
var ErrGrantNotFound = errors.New("table grant not found")

// TableGrantStore wraps Store to implement storage.TableGrantStore over
// table_grants (EXC-370). Whether exposure is enforced is not stored here:
// it is on for every project, switched only installation-wide (EXC-400).
type TableGrantStore struct{ s *Store }

func NewTableGrants(s *Store) *TableGrantStore { return &TableGrantStore{s: s} }

// TableGrants on *Store satisfies storage.PlatformStore.TableGrants.
func (s *Store) TableGrants() storage.TableGrantStore { return NewTableGrants(s) }

const grantColumns = `id, project_id, resource, operations, role_name, enabled, created_at, updated_at`

func (g *TableGrantStore) ListGrants(ctx context.Context, projectID string) ([]domain.TableGrant, error) {
	rows, err := g.s.db.QueryContext(ctx, `
		SELECT `+grantColumns+`
		FROM table_grants
		WHERE project_id = $1
		ORDER BY resource, role_name`, projectID)
	if err != nil {
		return nil, fmt.Errorf("query table_grants: %w", err)
	}
	defer rows.Close()

	out := []domain.TableGrant{}
	for rows.Next() {
		grant, err := scanGrant(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *grant)
	}
	return out, rows.Err()
}

func (g *TableGrantStore) GetGrant(ctx context.Context, projectID, id string) (*domain.TableGrant, error) {
	row := g.s.db.QueryRowContext(ctx, `
		SELECT `+grantColumns+`
		FROM table_grants WHERE project_id = $1 AND id = $2`, projectID, id)
	grant, err := scanGrant(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrGrantNotFound
	}
	return grant, err
}

// UpsertGrant inserts or replaces a grant. The WHERE on DO UPDATE is the same
// ownership guard the policy store uses (SEC-H1): a conflicting id owned by a
// different project updates zero rows instead of being hijacked.
func (g *TableGrantStore) UpsertGrant(ctx context.Context, grant *domain.TableGrant) error {
	ops := operationsToStrings(grant.Operations)
	res, err := g.s.db.ExecContext(ctx, `
		INSERT INTO table_grants (id, project_id, resource, operations, role_name,
		                          enabled, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, NOW(), NOW())
		ON CONFLICT (id) DO UPDATE SET
			resource   = EXCLUDED.resource,
			operations = EXCLUDED.operations,
			role_name  = EXCLUDED.role_name,
			enabled    = EXCLUDED.enabled,
			updated_at = NOW()
		WHERE table_grants.project_id = EXCLUDED.project_id`,
		grant.ID, grant.ProjectID, grant.Resource, pq.Array(ops), grant.Role, grant.Enabled)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("table grant %q is owned by another project", grant.ID)
	}
	return nil
}

func (g *TableGrantStore) DeleteGrant(ctx context.Context, projectID, id string) error {
	res, err := g.s.db.ExecContext(ctx,
		`DELETE FROM table_grants WHERE project_id = $1 AND id = $2`, projectID, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrGrantNotFound
	}
	return nil
}

func scanGrant(r rowScanner) (*domain.TableGrant, error) {
	var (
		grant domain.TableGrant
		ops   pq.StringArray
	)
	if err := r.Scan(&grant.ID, &grant.ProjectID, &grant.Resource, &ops, &grant.Role,
		&grant.Enabled, &grant.CreatedAt, &grant.UpdatedAt); err != nil {
		return nil, err
	}
	grant.Operations = stringsToOperations(ops)
	return &grant, nil
}

var _ storage.TableGrantStore = (*TableGrantStore)(nil)
