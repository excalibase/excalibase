package postgres

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/excalibase/provisioning-poc/internal/logdelete"
)

// EnqueueProjectLogDeletion queues a deleted project's log deletion (EXC-678).
// A retried teardown keeps the row it queued first; only a project id used
// again after its earlier deletion finished is queued afresh.
func (s *Store) EnqueueProjectLogDeletion(ctx context.Context, d logdelete.Deletion) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO project_log_deletions (project_id, selector, start_at, end_at, next_attempt_at)
		VALUES ($1, $2, $3, $4, COALESCE($5, now()))
		ON CONFLICT (project_id) DO UPDATE SET
			selector = EXCLUDED.selector, start_at = EXCLUDED.start_at, end_at = EXCLUDED.end_at, state = 'pending',
			attempts = 0, resends = 0, next_attempt_at = EXCLUDED.next_attempt_at, lease_until = NULL,
			requested_at = NULL, done_at = NULL, last_error = '', queued_at = now()
		WHERE project_log_deletions.state IN ('done', 'refused', 'lost')`,
		d.ProjectID, d.Selector, d.Start, d.End, nullTime(d.NextAttemptAt))
	if err != nil {
		return fmt.Errorf("queue log deletion of %s: %w", d.ProjectID, err)
	}
	return nil
}

// ClaimDueProjectLogDeletions leases up to limit due rows; a row another
// replica holds, or holds a live lease on, is skipped.
func (s *Store) ClaimDueProjectLogDeletions(ctx context.Context, now time.Time, lease time.Duration, limit int) ([]logdelete.Deletion, error) {
	rows, err := s.db.QueryContext(ctx, `
		UPDATE project_log_deletions SET lease_until = $2
		WHERE project_id IN (
			SELECT project_id FROM project_log_deletions
			WHERE state IN ('pending', 'requested') AND next_attempt_at <= $1
				AND (lease_until IS NULL OR lease_until <= $1)
			ORDER BY next_attempt_at
			LIMIT $3
			FOR UPDATE SKIP LOCKED)
		RETURNING project_id, selector, start_at, end_at, state, attempts, resends, next_attempt_at,
			requested_at, done_at, last_error, lease_until`,
		now, now.Add(lease), limit)
	if err != nil {
		return nil, fmt.Errorf("claim log deletions: %w", err)
	}
	defer rows.Close()
	var due []logdelete.Deletion
	for rows.Next() {
		var d logdelete.Deletion
		var state string
		var requested, done sql.NullTime
		if err := rows.Scan(&d.ProjectID, &d.Selector, &d.Start, &d.End, &state, &d.Attempts, &d.Resends, &d.NextAttemptAt,
			&requested, &done, &d.LastError, &d.LeaseUntil); err != nil {
			return nil, fmt.Errorf("scan log deletion: %w", err)
		}
		d.State = logdelete.State(state)
		d.RequestedAt, d.DoneAt = requested.Time, done.Time
		due = append(due, d)
	}
	return due, rows.Err()
}

// SaveProjectLogDeletion writes a worked row's progress and ends its lease,
// only while the lease it was claimed under still holds the row.
func (s *Store) SaveProjectLogDeletion(ctx context.Context, d logdelete.Deletion) error {
	result, err := s.db.ExecContext(ctx, `
		UPDATE project_log_deletions SET state = $2, attempts = $3, resends = $4, next_attempt_at = $5,
			requested_at = $6, done_at = $7, last_error = $8, lease_until = NULL
		WHERE project_id = $1 AND lease_until = $9`,
		d.ProjectID, string(d.State), d.Attempts, d.Resends, d.NextAttemptAt, nullTime(d.RequestedAt),
		nullTime(d.DoneAt), d.LastError, d.LeaseUntil)
	if err != nil {
		return fmt.Errorf("save log deletion of %s: %w", d.ProjectID, err)
	}
	if saved, err := result.RowsAffected(); err != nil || saved == 0 {
		return fmt.Errorf("save log deletion of %s: %w", d.ProjectID, logdelete.ErrLeaseLost)
	}
	return nil
}

func nullTime(at time.Time) sql.NullTime {
	return sql.NullTime{Time: at, Valid: !at.IsZero()}
}
