package postgres

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
)

func (s *Store) TouchProjectActivity(ctx context.Context, projectID, source string, seenAt time.Time) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO project_activity (project_id, last_seen_at, last_seen_source)
		 VALUES ($1, $2, $3)
		 ON CONFLICT (project_id) DO UPDATE SET
		    last_seen_at     = EXCLUDED.last_seen_at,
		    last_seen_source = EXCLUDED.last_seen_source`,
		projectID, seenAt.UTC(), source)
	return err
}

func (s *Store) GetProjectActivity(ctx context.Context, projectID string) (domain.ProjectActivity, bool, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT project_id, last_seen_at, last_seen_source FROM project_activity WHERE project_id = $1`, projectID)
	activity, err := scanProjectActivity(row)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.ProjectActivity{}, false, nil
	}
	if err != nil {
		return domain.ProjectActivity{}, false, err
	}
	return activity, true, nil
}

func (s *Store) ListProjectActivity(ctx context.Context) (map[string]domain.ProjectActivity, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT project_id, last_seen_at, last_seen_source FROM project_activity`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make(map[string]domain.ProjectActivity)
	for rows.Next() {
		activity, err := scanProjectActivity(rows)
		if err != nil {
			return nil, err
		}
		out[activity.ProjectID] = activity
	}
	return out, rows.Err()
}

func scanProjectActivity(r scanner) (domain.ProjectActivity, error) {
	var activity domain.ProjectActivity
	err := r.Scan(&activity.ProjectID, &activity.LastSeenAt, &activity.LastSeenSource)
	return activity, err
}
