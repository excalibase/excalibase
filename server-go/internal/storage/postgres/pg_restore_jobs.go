package postgres

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/storage"
)

type RestoreJobsStore struct{ s *Store }

func NewRestoreJobs(s *Store) *RestoreJobsStore { return &RestoreJobsStore{s: s} }

// RestoreJobs on *Store satisfies storage.PlatformStore.RestoreJobs.
func (s *Store) RestoreJobs() storage.RestoreJobStore { return NewRestoreJobs(s) }

func (r *RestoreJobsStore) UpsertRestoreJob(ctx context.Context, j *domain.RestoreJob) error {
	_, err := r.s.db.ExecContext(ctx, `
		INSERT INTO restore_jobs (id, source_project_id, new_project_id, new_project_name, status, current_step, target_kind, target_value, failure_reason, owner, heartbeat_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, NOW(), NOW())
		ON CONFLICT (id) DO UPDATE SET
			status = EXCLUDED.status,
			current_step = EXCLUDED.current_step,
			failure_reason = EXCLUDED.failure_reason,
			owner = EXCLUDED.owner,
			heartbeat_at = NOW(),
			updated_at = NOW()`,
		j.ID, j.SourceProjectID, j.NewProjectID, j.NewProjectName, j.Status, j.CurrentStep,
		j.TargetKind, j.TargetValue, j.FailureReason, j.Owner)
	return err
}

// UpdateRunningRestoreJob is the only write a driver makes after Start. The
// WHERE clause is the whole point: RUNNING and owner pin the write to a job
// this process still holds, so a driver whose job was swept away cannot
// resurrect it, and a terminal outcome can never be written over.
func (r *RestoreJobsStore) UpdateRunningRestoreJob(ctx context.Context, j *domain.RestoreJob, owner string) (bool, error) {
	res, err := r.s.db.ExecContext(ctx, `
		UPDATE restore_jobs
		SET status = $1, current_step = $2, failure_reason = $3, heartbeat_at = NOW(), updated_at = NOW()
		WHERE id = $4 AND status = 'RUNNING' AND owner = $5`,
		j.Status, j.CurrentStep, j.FailureReason, j.ID, owner)
	if err != nil {
		return false, fmt.Errorf("update restore job %s: %w", j.ID, err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("update restore job %s: %w", j.ID, err)
	}
	return affected == 1, nil
}

// HeartbeatRestoreJob proves the owner is still alive. It reports false once
// the job is no longer ours, which is how a driver learns it was swept.
func (r *RestoreJobsStore) HeartbeatRestoreJob(ctx context.Context, id, owner string) (bool, error) {
	res, err := r.s.db.ExecContext(ctx, `
		UPDATE restore_jobs SET heartbeat_at = NOW()
		WHERE id = $1 AND status = 'RUNNING' AND owner = $2`, id, owner)
	if err != nil {
		return false, fmt.Errorf("heartbeat restore job %s: %w", id, err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("heartbeat restore job %s: %w", id, err)
	}
	return affected == 1, nil
}

// FailAbandonedRestoreJobs fails running jobs whose owner has gone quiet.
// Jobs this process owns are excluded — it knows it is alive — and so is
// every job whose heartbeat is still fresh, which is what keeps a rolling
// deploy from failing the restores its peers are driving.
func (r *RestoreJobsStore) FailAbandonedRestoreJobs(ctx context.Context, owner string, staleAfter time.Duration, reason string) ([]domain.RestoreJob, error) {
	rows, err := r.s.db.QueryContext(ctx, `
		UPDATE restore_jobs
		SET status = 'FAILED', failure_reason = $1, updated_at = NOW()
		WHERE status = 'RUNNING' AND owner <> $2 AND heartbeat_at < NOW() - $3::interval
		RETURNING id, new_project_id`,
		reason, owner, fmt.Sprintf("%d milliseconds", staleAfter.Milliseconds()))
	if err != nil {
		return nil, fmt.Errorf("fail abandoned restore jobs: %w", err)
	}
	defer rows.Close()

	failed := []domain.RestoreJob{}
	for rows.Next() {
		var j domain.RestoreJob
		if err := rows.Scan(&j.ID, &j.NewProjectID); err != nil {
			return nil, fmt.Errorf("fail abandoned restore jobs: %w", err)
		}
		j.Status = domain.RestoreStatusFailed
		j.FailureReason = reason
		failed = append(failed, j)
	}
	return failed, rows.Err()
}

// FindRestoreJob resolves a job within one project. The row names two
// projects — the source that was dumped and the new project it was restored
// into — and both are legitimate readers of their own restore, so either side
// matches. Every other project gets no row at all: scoping here, in the query,
// means no caller can reach a job by id alone (EXC-399).
func (r *RestoreJobsStore) FindRestoreJob(ctx context.Context, projectID, id string) (*domain.RestoreJob, error) {
	row := r.s.db.QueryRowContext(ctx, `
		SELECT id, source_project_id, new_project_id, new_project_name, status, current_step, target_kind, target_value, failure_reason, owner, heartbeat_at, created_at, updated_at
		FROM restore_jobs
		WHERE id = $1 AND ($2 IN (source_project_id, new_project_id))`, id, projectID)
	return scanPgRestoreJob(row)
}

func (r *RestoreJobsStore) ListRunningRestoreJobs(ctx context.Context) ([]domain.RestoreJob, error) {
	rows, err := r.s.db.QueryContext(ctx, `
		SELECT id, source_project_id, new_project_id, new_project_name, status, current_step, target_kind, target_value, failure_reason, owner, heartbeat_at, created_at, updated_at
		FROM restore_jobs WHERE status = 'RUNNING'`)
	if err != nil {
		return nil, fmt.Errorf("query restore_jobs: %w", err)
	}
	defer rows.Close()

	out := []domain.RestoreJob{}
	for rows.Next() {
		j, err := scanPgRestoreJob(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *j)
	}
	return out, rows.Err()
}

func scanPgRestoreJob(s scannable) (*domain.RestoreJob, error) {
	var j domain.RestoreJob
	var name, step, val, reason, owner sql.NullString
	var created, updated, heartbeat time.Time
	err := s.Scan(&j.ID, &j.SourceProjectID, &j.NewProjectID, &name, &j.Status,
		&step, &j.TargetKind, &val, &reason, &owner, &heartbeat, &created, &updated)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if name.Valid {
		j.NewProjectName = name.String
	}
	if step.Valid {
		j.CurrentStep = step.String
	}
	if val.Valid {
		j.TargetValue = val.String
	}
	if reason.Valid {
		j.FailureReason = reason.String
	}
	if owner.Valid {
		j.Owner = owner.String
	}
	j.HeartbeatAt = heartbeat.UTC().Format(time.RFC3339)
	j.CreatedAt = created.UTC().Format(time.RFC3339)
	j.UpdatedAt = updated.UTC().Format(time.RFC3339)
	return &j, nil
}
