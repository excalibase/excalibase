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
		INSERT INTO restore_jobs (id, source_project_id, new_project_id, status, current_step, target_kind, target_value, failure_reason, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, NOW())
		ON CONFLICT (id) DO UPDATE SET
			status = EXCLUDED.status,
			current_step = EXCLUDED.current_step,
			failure_reason = EXCLUDED.failure_reason,
			updated_at = NOW()`,
		j.ID, j.SourceProjectID, j.NewProjectID, j.Status, j.CurrentStep,
		j.TargetKind, j.TargetValue, j.FailureReason)
	return err
}

func (r *RestoreJobsStore) FindRestoreJob(ctx context.Context, id string) (*domain.RestoreJob, error) {
	row := r.s.db.QueryRowContext(ctx, `
		SELECT id, source_project_id, new_project_id, status, current_step, target_kind, target_value, failure_reason, created_at, updated_at
		FROM restore_jobs WHERE id = $1`, id)
	return scanPgRestoreJob(row)
}

func (r *RestoreJobsStore) ListRunningRestoreJobs(ctx context.Context) ([]domain.RestoreJob, error) {
	rows, err := r.s.db.QueryContext(ctx, `
		SELECT id, source_project_id, new_project_id, status, current_step, target_kind, target_value, failure_reason, created_at, updated_at
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
	var step, val, reason sql.NullString
	var created, updated time.Time
	err := s.Scan(&j.ID, &j.SourceProjectID, &j.NewProjectID, &j.Status,
		&step, &j.TargetKind, &val, &reason, &created, &updated)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
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
	j.CreatedAt = created.UTC().Format(time.RFC3339)
	j.UpdatedAt = updated.UTC().Format(time.RFC3339)
	return &j, nil
}
