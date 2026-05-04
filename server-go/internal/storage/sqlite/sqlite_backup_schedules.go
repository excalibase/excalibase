package sqlite

import (
	"context"
	"fmt"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/storage"
)

type BackupSchedulesStore struct{ s *Store }

func NewBackupSchedules(s *Store) *BackupSchedulesStore { return &BackupSchedulesStore{s: s} }

// BackupSchedules on *Store satisfies storage.PlatformStore.BackupSchedules.
func (s *Store) BackupSchedules() storage.BackupScheduleStore { return NewBackupSchedules(s) }

func (b *BackupSchedulesStore) UpsertSchedule(ctx context.Context, sch *domain.BackupSchedule) error {
	enabled := 0
	if sch.Enabled {
		enabled = 1
	}
	_, err := b.s.db.ExecContext(ctx, `
		INSERT INTO backup_schedules (project_id, cron_spec, retention_days, enabled, updated_at)
		VALUES (?, ?, ?, ?, CURRENT_TIMESTAMP)
		ON CONFLICT(project_id) DO UPDATE SET
			cron_spec = excluded.cron_spec,
			retention_days = excluded.retention_days,
			enabled = excluded.enabled,
			updated_at = CURRENT_TIMESTAMP`,
		sch.ProjectID, sch.Cron, sch.RetentionDays, enabled)
	return err
}

func (b *BackupSchedulesStore) ListEnabledSchedules(ctx context.Context) ([]domain.BackupSchedule, error) {
	rows, err := b.s.db.QueryContext(ctx, `
		SELECT project_id, cron_spec, retention_days, enabled
		FROM backup_schedules WHERE enabled = 1`)
	if err != nil {
		return nil, fmt.Errorf("query backup_schedules: %w", err)
	}
	defer rows.Close()

	out := []domain.BackupSchedule{}
	for rows.Next() {
		var sch domain.BackupSchedule
		var enabledInt int
		if err := rows.Scan(&sch.ProjectID, &sch.Cron, &sch.RetentionDays, &enabledInt); err != nil {
			return nil, err
		}
		sch.Enabled = enabledInt == 1
		out = append(out, sch)
	}
	return out, rows.Err()
}

func (b *BackupSchedulesStore) DeleteSchedule(ctx context.Context, projectID string) error {
	_, err := b.s.db.ExecContext(ctx, `DELETE FROM backup_schedules WHERE project_id = ?`, projectID)
	return err
}
