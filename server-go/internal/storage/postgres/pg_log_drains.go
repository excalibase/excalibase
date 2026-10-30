package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
)

// logDrainLockSpace serializes drain creation per project (EXC-658), so the
// per-project limit holds under concurrent creates.
const logDrainLockSpace = 658

const logDrainColumns = `id, project_id, name, kind, datadog_site, url, header_name, enabled, status,
	last_error, consecutive_failures, cursor_ns, lease_to, lease_lines, next_attempt_at, last_success_at,
	sent_lines, dropped_lines, created_by, created_at, updated_at`

const insertLogDrainSQL = `INSERT INTO log_drains (id, project_id, name, kind, datadog_site, url, header_name, enabled, status,
	last_error, consecutive_failures, cursor_ns, lease_to, lease_lines, next_attempt_at, last_success_at,
	sent_lines, dropped_lines, created_by, created_at, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,0,0,$13,NULL,0,0,$14,$15,$16)`

func scanLogDrain(row scanner) (*domain.LogDrain, error) {
	var drain domain.LogDrain
	var lastSuccess sql.NullTime
	if err := row.Scan(&drain.ID, &drain.ProjectID, &drain.Name, &drain.Kind, &drain.DatadogSite, &drain.URL,
		&drain.HeaderName, &drain.Enabled, &drain.Status, &drain.LastError, &drain.ConsecutiveFailures,
		&drain.CursorNS, &drain.LeaseTo, &drain.LeaseLines, &drain.NextAttemptAt, &lastSuccess, &drain.SentLines, &drain.DroppedLines,
		&drain.CreatedBy, &drain.CreatedAt, &drain.UpdatedAt); err != nil {
		return nil, err
	}
	if lastSuccess.Valid {
		at := lastSuccess.Time
		drain.LastSuccessAt = &at
	}
	return &drain, nil
}

// CreateLogDrain inserts the drain unless its project already has limit drains.
func (s *Store) CreateLogDrain(ctx context.Context, drain *domain.LogDrain, limit int) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin create log drain: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // rollback after a commit is a no-op
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock($1, hashtext($2))`, logDrainLockSpace, drain.ProjectID); err != nil {
		return fmt.Errorf("lock log drains: %w", err)
	}
	var count int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM log_drains WHERE project_id = $1`, drain.ProjectID).Scan(&count); err != nil {
		return fmt.Errorf("count log drains: %w", err)
	}
	if count >= limit {
		return domain.ErrLogDrainLimit
	}
	if _, err := tx.ExecContext(ctx, insertLogDrainSQL,
		drain.ID, drain.ProjectID, drain.Name, drain.Kind, drain.DatadogSite, drain.URL, drain.HeaderName,
		drain.Enabled, drain.Status, drain.LastError, drain.ConsecutiveFailures, drain.CursorNS, drain.NextAttemptAt,
		drain.CreatedBy, drain.CreatedAt, drain.UpdatedAt); err != nil {
		return fmt.Errorf("insert log drain: %w", err)
	}
	return tx.Commit()
}

func (s *Store) queryLogDrains(ctx context.Context, query string, args ...any) ([]domain.LogDrain, error) {
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("query log drains: %w", err)
	}
	defer rows.Close()
	out := []domain.LogDrain{}
	for rows.Next() {
		drain, err := scanLogDrain(rows)
		if err != nil {
			return nil, fmt.Errorf("scan log drain: %w", err)
		}
		out = append(out, *drain)
	}
	return out, rows.Err()
}

func (s *Store) ListLogDrains(ctx context.Context, projectID string) ([]domain.LogDrain, error) {
	return s.queryLogDrains(ctx, `SELECT `+logDrainColumns+` FROM log_drains WHERE project_id = $1 ORDER BY created_at, id`, projectID)
}

func (s *Store) findLogDrain(ctx context.Context, query string, args ...any) (*domain.LogDrain, error) {
	drain, err := scanLogDrain(s.db.QueryRowContext(ctx, query, args...))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, domain.ErrLogDrainNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("read log drain: %w", err)
	}
	return drain, nil
}

// GetLogDrain reads a drain only through its own project.
func (s *Store) GetLogDrain(ctx context.Context, projectID, id string) (*domain.LogDrain, error) {
	return s.findLogDrain(ctx, `SELECT `+logDrainColumns+` FROM log_drains WHERE project_id = $1 AND id = $2`, projectID, id)
}

// FindLogDrain reads a drain by id alone, for the forwarder's acks.
func (s *Store) FindLogDrain(ctx context.Context, id string) (*domain.LogDrain, error) {
	return s.findLogDrain(ctx, `SELECT `+logDrainColumns+` FROM log_drains WHERE id = $1`, id)
}

// UpdateLogDrain writes an Admin's change to the drain's settings only; its
// delivery state is the forwarder's, and SetLogDrainEnabled's.
func (s *Store) UpdateLogDrain(ctx context.Context, drain *domain.LogDrain) error {
	res, err := s.db.ExecContext(ctx, `UPDATE log_drains SET name = $3, datadog_site = $4, url = $5, header_name = $6,
		updated_at = $7 WHERE project_id = $1 AND id = $2`,
		drain.ProjectID, drain.ID, drain.Name, drain.DatadogSite, drain.URL, drain.HeaderName, drain.UpdatedAt)
	return oneRow(res, err, "update log drain")
}

// SetLogDrainEnabled turns a drain on from cursor with a clean slate, or off.
func (s *Store) SetLogDrainEnabled(ctx context.Context, projectID, id string, enabled bool, cursor int64, at time.Time) error {
	res, err := s.db.ExecContext(ctx, `UPDATE log_drains SET enabled = $3,
		status = CASE WHEN $3 THEN 'active' ELSE 'disabled' END,
		last_error = '', consecutive_failures = 0, lease_to = 0, lease_lines = 0,
		cursor_ns = CASE WHEN $3 THEN $4 ELSE cursor_ns END,
		next_attempt_at = $5, updated_at = $5
		WHERE project_id = $1 AND id = $2`, projectID, id, enabled, cursor, at)
	return oneRow(res, err, "set log drain enabled")
}

func (s *Store) DeleteLogDrain(ctx context.Context, projectID, id string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM log_drains WHERE project_id = $1 AND id = $2`, projectID, id)
	return oneRow(res, err, "delete log drain")
}

func oneRow(res sql.Result, err error, action string) error {
	if err != nil {
		return fmt.Errorf("%s: %w", action, err)
	}
	if n, err := res.RowsAffected(); err != nil || n == 0 {
		return domain.ErrLogDrainNotFound
	}
	return nil
}

// DueLogDrains are enabled drains whose next attempt has come, oldest first.
func (s *Store) DueLogDrains(ctx context.Context, now time.Time, limit int) ([]domain.LogDrain, error) {
	return s.queryLogDrains(ctx, `SELECT `+logDrainColumns+` FROM log_drains
		WHERE enabled AND next_attempt_at <= $1 ORDER BY next_attempt_at, id LIMIT $2`, now, limit)
}

// LeaseLogDrain hands a batch out, only while the drain is due and unmoved,
// so two pulls never hand out the same drain.
func (s *Store) LeaseLogDrain(ctx context.Context, lease domain.LogDrainLease) (bool, error) {
	res, err := s.db.ExecContext(ctx, `UPDATE log_drains SET lease_to = $3, lease_lines = $4, next_attempt_at = $5
		WHERE id = $1 AND cursor_ns = $2 AND enabled AND next_attempt_at <= $6`,
		lease.ID, lease.From, lease.To, lease.Lines, lease.Until, lease.Now)
	return applied(res, err, "lease log drain")
}

// AdvanceLogDrain moves the cursor only from where the caller saw it; a
// delivered batch must be the one leased.
func (s *Store) AdvanceLogDrain(ctx context.Context, adv domain.LogDrainAdvance) (bool, error) {
	res, err := s.db.ExecContext(ctx, `UPDATE log_drains SET cursor_ns = $3, sent_lines = sent_lines + $4,
		dropped_lines = dropped_lines + $5, next_attempt_at = $6, lease_to = 0, lease_lines = 0,
		consecutive_failures = CASE WHEN $7 THEN 0 ELSE consecutive_failures END,
		last_error = CASE WHEN $7 THEN '' ELSE last_error END,
		status = CASE WHEN $7 THEN 'active' ELSE status END,
		last_success_at = CASE WHEN $7 THEN now() ELSE last_success_at END
		WHERE id = $1 AND cursor_ns = $2 AND enabled AND (NOT $7 OR lease_to = $3)`,
		adv.ID, adv.From, adv.To, adv.Sent, adv.Dropped, adv.Next, adv.Delivered)
	return applied(res, err, "advance log drain")
}

// FailLogDrain counts a failure at the cursor (and lease) the caller saw;
// Disable turns it off.
func (s *Store) FailLogDrain(ctx context.Context, failure domain.LogDrainFailure) (bool, error) {
	res, err := s.db.ExecContext(ctx, `UPDATE log_drains SET consecutive_failures = consecutive_failures + 1,
		last_error = $3, next_attempt_at = $4, lease_to = 0, lease_lines = 0,
		status = CASE WHEN $5 THEN 'disabled' ELSE 'failing' END,
		enabled = NOT $5
		WHERE id = $1 AND cursor_ns = $2 AND enabled AND ($6::bigint = 0 OR lease_to = $6::bigint)`,
		failure.ID, failure.From, failure.Error, failure.Next, failure.Disable, failure.LeaseTo)
	return applied(res, err, "fail log drain")
}

func (s *Store) DeferLogDrain(ctx context.Context, id string, next time.Time) error {
	if _, err := s.db.ExecContext(ctx, `UPDATE log_drains SET next_attempt_at = $2 WHERE id = $1`, id, next); err != nil {
		return fmt.Errorf("defer log drain: %w", err)
	}
	return nil
}

func applied(res sql.Result, err error, action string) (bool, error) {
	if err != nil {
		return false, fmt.Errorf("%s: %w", action, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("%s: %w", action, err)
	}
	return n == 1, nil
}
