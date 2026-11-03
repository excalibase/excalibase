package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/storage"
	"github.com/lib/pq"
)

var _ storage.MajorUpgradeStore = (*Store)(nil)

const majorUpgradeColumns = `id, project_id, engine, from_version, to_version, status, step, safety_backup_id,
	converted, error, warnings, requested_by, started_at, writes_stopped_at, converted_at, finished_at, heartbeat_at, owner`

// StartMajorUpgrade inserts a RUNNING run; the partial unique index refuses a
// second one for the same project.
func (s *Store) StartMajorUpgrade(ctx context.Context, run *domain.MajorUpgrade) error {
	if run.StartedAt.IsZero() {
		run.StartedAt = time.Now().UTC()
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO database_major_upgrades
		(id, project_id, engine, from_version, to_version, status, step, requested_by, started_at, heartbeat_at, owner)
		VALUES ($1, $2, $3, $4, $5, 'RUNNING', $6, $7, $8, $8, $9)`,
		run.ID, run.ProjectID, run.Engine, run.FromVersion, run.ToVersion, run.Step, run.RequestedBy, run.StartedAt, run.Owner)
	if isPQCode(err, uniqueViolation) {
		return storage.ErrMajorUpgradeRunning
	}
	if err != nil {
		return fmt.Errorf("record major upgrade: %w", err)
	}
	run.Status = domain.MajorUpgradeRunning
	run.HeartbeatAt = run.StartedAt
	return nil
}

// UpdateMajorUpgrade writes everything a run changes except converted and
// owner, and the project's status with it, in one transaction.
func (s *Store) UpdateMajorUpgrade(ctx context.Context, run *domain.MajorUpgrade, project *storage.MajorUpgradeProject) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin update major upgrade: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // rollback after a commit is a no-op
	if err := updateOwnedRun(ctx, tx, run); err != nil {
		return err
	}
	if project != nil {
		if _, err := tx.ExecContext(ctx, `UPDATE database_instances SET status = $2, current_step = $3,
			failure_reason = $4, updated_at = now() WHERE project_id = $1`,
			run.ProjectID, project.Status, project.Step, project.Reason); err != nil {
			return fmt.Errorf("record %s %s: %w", run.ProjectID, project.Status, err)
		}
	}
	return tx.Commit()
}

// updateOwnedRun writes run while its owner holds it RUNNING; a converted
// run ends only as COMPLETED.
func updateOwnedRun(ctx context.Context, tx *sql.Tx, run *domain.MajorUpgrade) error {
	warnings := run.Warnings
	if warnings == nil {
		warnings = []string{}
	}
	res, err := tx.ExecContext(ctx, `UPDATE database_major_upgrades SET
		status = $2, step = $3, safety_backup_id = $4, error = $5, warnings = $6, finished_at = $7,
		writes_stopped_at = $8
		WHERE id = $1 AND status = 'RUNNING' AND owner = $9
		  AND (NOT converted OR $2 IN ('RUNNING', 'COMPLETED'))`,
		run.ID, run.Status, run.Step, run.SafetyBackupID, run.Error, pq.Array(warnings), run.FinishedAt, run.WritesStoppedAt, run.Owner)
	if err != nil {
		return fmt.Errorf("update major upgrade %s: %w", run.ID, err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("update major upgrade %s: %w", run.ID, storage.ErrMajorUpgradeNotRunning)
	}
	return nil
}

// TouchMajorUpgrade moves a running run's heartbeat for its owner.
func (s *Store) TouchMajorUpgrade(ctx context.Context, id, owner string, at time.Time) error {
	res, err := s.db.ExecContext(ctx, `UPDATE database_major_upgrades SET heartbeat_at = $3
		WHERE id = $1 AND owner = $2 AND status = 'RUNNING'`, id, owner, at.UTC())
	if err != nil {
		return fmt.Errorf("touch major upgrade %s: %w", id, err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return storage.ErrMajorUpgradeNotRunning
	}
	return nil
}

// MarkMajorUpgradeConverted flips converted and the project's version together.
func (s *Store) MarkMajorUpgradeConverted(ctx context.Context, id, projectID, toVersion, owner string, at time.Time) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin mark converted: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // rollback after a commit is a no-op
	res, err := tx.ExecContext(ctx, `UPDATE database_major_upgrades SET converted = true, converted_at = $3, heartbeat_at = $3
		WHERE id = $1 AND project_id = $2 AND owner = $4 AND status = 'RUNNING' AND NOT converted`, id, projectID, at.UTC(), owner)
	if err != nil {
		return fmt.Errorf("mark major upgrade %s converted: %w", id, err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return storage.ErrMajorUpgradeNotRunning
	}
	if _, err := tx.ExecContext(ctx, `UPDATE database_instances SET postgres_version = $2, updated_at = now()
		WHERE project_id = $1`, projectID, toVersion); err != nil {
		return fmt.Errorf("record %s on postgres %s: %w", projectID, toVersion, err)
	}
	return tx.Commit()
}

// ClaimMajorUpgrade hands a stale RUNNING run to owner in one statement.
func (s *Store) ClaimMajorUpgrade(ctx context.Context, id, owner string, staleBefore, at time.Time) (*domain.MajorUpgrade, error) {
	row := s.db.QueryRowContext(ctx, `UPDATE database_major_upgrades SET owner = $2, heartbeat_at = $4
		WHERE id = $1 AND status = 'RUNNING' AND heartbeat_at < $3
		RETURNING `+majorUpgradeColumns, id, owner, staleBefore.UTC(), at.UTC())
	run, err := scanMajorUpgrade(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("claim major upgrade %s: %w", id, err)
	}
	return &run, nil
}

// LatestMajorUpgrade is the project's newest run, or nil.
func (s *Store) LatestMajorUpgrade(ctx context.Context, projectID string) (*domain.MajorUpgrade, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+majorUpgradeColumns+` FROM database_major_upgrades
		WHERE project_id = $1 ORDER BY started_at DESC, id DESC LIMIT 1`, projectID)
	run, err := scanMajorUpgrade(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read major upgrade of %s: %w", projectID, err)
	}
	return &run, nil
}

// ConvertedMajorUpgrades lists the runs that moved the data, oldest first.
func (s *Store) ConvertedMajorUpgrades(ctx context.Context, projectID string) ([]domain.MajorUpgrade, error) {
	return s.queryMajorUpgrades(ctx, `SELECT `+majorUpgradeColumns+` FROM database_major_upgrades
		WHERE project_id = $1 AND converted ORDER BY converted_at, id`, projectID)
}

// RunningMajorUpgrades lists every unfinished run.
func (s *Store) RunningMajorUpgrades(ctx context.Context) ([]domain.MajorUpgrade, error) {
	return s.queryMajorUpgrades(ctx, `SELECT `+majorUpgradeColumns+` FROM database_major_upgrades
		WHERE status = 'RUNNING' ORDER BY started_at, id`)
}

func (s *Store) queryMajorUpgrades(ctx context.Context, query string, args ...interface{}) ([]domain.MajorUpgrade, error) {
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("query major upgrades: %w", err)
	}
	defer rows.Close()
	runs := []domain.MajorUpgrade{}
	for rows.Next() {
		run, err := scanMajorUpgrade(rows)
		if err != nil {
			return nil, err
		}
		runs = append(runs, run)
	}
	return runs, rows.Err()
}

func scanMajorUpgrade(row scanner) (domain.MajorUpgrade, error) {
	var run domain.MajorUpgrade
	var warnings []string
	var stoppedAt, convertedAt, finishedAt sql.NullTime
	err := row.Scan(&run.ID, &run.ProjectID, &run.Engine, &run.FromVersion, &run.ToVersion, &run.Status, &run.Step,
		&run.SafetyBackupID, &run.Converted, &run.Error, pq.Array(&warnings), &run.RequestedBy, &run.StartedAt,
		&stoppedAt, &convertedAt, &finishedAt, &run.HeartbeatAt, &run.Owner)
	if err != nil {
		return run, err
	}
	if len(warnings) > 0 {
		run.Warnings = warnings
	}
	run.WritesStoppedAt = optionalTime(stoppedAt)
	run.ConvertedAt = optionalTime(convertedAt)
	run.FinishedAt = optionalTime(finishedAt)
	return run, nil
}

func optionalTime(value sql.NullTime) *time.Time {
	if !value.Valid {
		return nil
	}
	at := value.Time
	return &at
}
