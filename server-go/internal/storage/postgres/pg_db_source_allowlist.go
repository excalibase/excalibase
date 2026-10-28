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

var _ storage.DBSourceAllowListStore = (*Store)(nil)

// GetDBSourceAllowList returns the project's database allow-list (EXC-665).
func (s *Store) GetDBSourceAllowList(ctx context.Context, projectID string) (domain.DBSourceAllowList, error) {
	list := domain.DBSourceAllowList{ProjectID: projectID}
	var access string
	err := s.db.QueryRowContext(ctx,
		`SELECT access, sources FROM database_endpoint_allowed_sources WHERE project_id = $1`, projectID).
		Scan(&access, pq.Array(&list.Sources))
	if errors.Is(err, sql.ErrNoRows) {
		return list, nil
	}
	if err != nil {
		return domain.DBSourceAllowList{}, fmt.Errorf("read database allow-list: %w", err)
	}
	list.Access = domain.DBSourceAccess(access)
	return list, nil
}

// SetDBSourceAllowList records a chosen list; the table's checks back the
// domain's rules so no caller can store an unchosen or empty listed one.
func (s *Store) SetDBSourceAllowList(ctx context.Context, list domain.DBSourceAllowList) (domain.DBSourceAllowList, error) {
	if !list.Chosen() {
		return domain.DBSourceAllowList{}, fmt.Errorf("record database allow-list: access %q is not a choice", list.Access)
	}
	sources := list.Sources
	if sources == nil {
		sources = []string{}
	}
	saved := domain.DBSourceAllowList{ProjectID: list.ProjectID}
	var access string
	err := s.db.QueryRowContext(ctx, `
INSERT INTO database_endpoint_allowed_sources (project_id, access, sources, updated_at)
VALUES ($1, $2, $3, NOW())
ON CONFLICT (project_id) DO UPDATE SET access = EXCLUDED.access, sources = EXCLUDED.sources, updated_at = NOW()
RETURNING access, sources`, list.ProjectID, string(list.Access), pq.Array(sources)).
		Scan(&access, pq.Array(&saved.Sources))
	if err != nil {
		return domain.DBSourceAllowList{}, fmt.Errorf("record database allow-list: %w", err)
	}
	saved.Access = domain.DBSourceAccess(access)
	return saved, nil
}

// DeleteDBSourceAllowList forgets the project's choice.
func (s *Store) DeleteDBSourceAllowList(ctx context.Context, projectID string) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM database_endpoint_allowed_sources WHERE project_id = $1`, projectID); err != nil {
		return fmt.Errorf("delete database allow-list: %w", err)
	}
	return nil
}
