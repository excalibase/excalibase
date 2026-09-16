package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/excalibase/provisioning-poc/internal/storage"
	"github.com/lib/pq"
)

// GetCorsOrigins returns the project's browser-origin allowlist; a project
// without a row reads as empty, which the data plane treats as "no CORS".
func (s *Store) GetCorsOrigins(ctx context.Context, projectID string) ([]string, error) {
	origins := []string{}
	err := s.db.QueryRowContext(ctx,
		`SELECT allowed_origins FROM project_cors_settings WHERE project_id = $1`,
		projectID,
	).Scan(pq.Array(&origins))
	if errors.Is(err, sql.ErrNoRows) {
		return []string{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read cors allowlist: %w", err)
	}
	if origins == nil {
		origins = []string{}
	}
	return origins, nil
}

// SetCorsOrigins replaces the project's allowlist. nil clears it.
func (s *Store) SetCorsOrigins(ctx context.Context, projectID string, origins []string) error {
	if origins == nil {
		origins = []string{}
	}
	_, err := s.db.ExecContext(ctx, `
INSERT INTO project_cors_settings (project_id, allowed_origins, updated_at)
VALUES ($1, $2, $3)
ON CONFLICT (project_id) DO UPDATE SET
    allowed_origins = EXCLUDED.allowed_origins,
    updated_at      = EXCLUDED.updated_at`,
		projectID, pq.Array(origins), time.Now())
	if err != nil {
		return fmt.Errorf("write cors allowlist: %w", err)
	}
	return nil
}

var _ storage.ProjectCorsStore = (*Store)(nil)
