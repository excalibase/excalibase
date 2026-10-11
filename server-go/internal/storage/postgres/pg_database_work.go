package postgres

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// DatabaseWork is a project's last client-work reading (EXC-588); false when
// none was stored.
func (s *Store) DatabaseWork(ctx context.Context, projectID string) (int64, bool, error) {
	var work int64
	err := s.db.QueryRowContext(ctx, `SELECT work FROM project_database_work WHERE project_id = $1`, projectID).Scan(&work)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	return work, true, nil
}

// SaveDatabaseWork stores a project's client-work reading.
func (s *Store) SaveDatabaseWork(ctx context.Context, projectID string, work int64, readAt time.Time) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO project_database_work (project_id, work, read_at) VALUES ($1, $2, $3)
		ON CONFLICT (project_id) DO UPDATE SET work = EXCLUDED.work, read_at = EXCLUDED.read_at`,
		projectID, work, readAt.UTC())
	return err
}
