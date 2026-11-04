package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// HostLogBudgetUse is a project's stored log text on a UTC day (EXC-678).
func (s *Store) HostLogBudgetUse(ctx context.Context, projectID, day string) (int64, bool, error) {
	var bytes int64
	var noticed bool
	err := s.db.QueryRowContext(ctx, `SELECT bytes, noticed FROM host_log_budget WHERE project_id = $1 AND day = $2::date`,
		projectID, day).Scan(&bytes, &noticed)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, fmt.Errorf("host log budget of %s on %s: %w", projectID, day, err)
	}
	return bytes, noticed, nil
}

// AddHostLogBudgetUse adds a stored batch's bytes to the day; a notice, once given, stays.
func (s *Store) AddHostLogBudgetUse(ctx context.Context, projectID, day string, bytes int64, noticed bool) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO host_log_budget (project_id, day, bytes, noticed)
		VALUES ($1, $2::date, $3, $4)
		ON CONFLICT (project_id, day) DO UPDATE SET
			bytes = host_log_budget.bytes + EXCLUDED.bytes,
			noticed = host_log_budget.noticed OR EXCLUDED.noticed`, projectID, day, bytes, noticed)
	if err != nil {
		return fmt.Errorf("add host log budget of %s on %s: %w", projectID, day, err)
	}
	return nil
}

// PruneHostLogBudget forgets the days before before.
func (s *Store) PruneHostLogBudget(ctx context.Context, before string) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM host_log_budget WHERE day < $1::date`, before); err != nil {
		return fmt.Errorf("prune host log budget: %w", err)
	}
	return nil
}
