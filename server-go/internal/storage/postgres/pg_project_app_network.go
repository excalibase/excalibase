package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/excalibase/provisioning-poc/internal/storage"
)

// GetAppPrivateNetwork reads the project's setting; no row is off.
func (s *Store) GetAppPrivateNetwork(ctx context.Context, projectID string) (bool, error) {
	var enabled bool
	err := s.db.QueryRowContext(ctx,
		`SELECT private_network FROM project_app_network WHERE project_id = $1`, projectID,
	).Scan(&enabled)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("read app private network setting: %w", err)
	}
	return enabled, nil
}

// SetAppPrivateNetwork records the project's setting.
func (s *Store) SetAppPrivateNetwork(ctx context.Context, projectID string, enabled bool) error {
	_, err := s.db.ExecContext(ctx, `
INSERT INTO project_app_network (project_id, private_network, updated_at)
VALUES ($1, $2, $3)
ON CONFLICT (project_id) DO UPDATE SET
    private_network = EXCLUDED.private_network,
    updated_at      = EXCLUDED.updated_at`,
		projectID, enabled, time.Now())
	if err != nil {
		return fmt.Errorf("write app private network setting: %w", err)
	}
	return nil
}

var _ storage.ProjectAppNetworkStore = (*Store)(nil)
