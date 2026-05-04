package postgres

import (
	"context"
	"fmt"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/storage"
)

// BackupSchedulesStore wraps Store to implement
// storage.BackupScheduleStore. Pattern matches BackupRecordsStore —
// the wrapper avoids method-name collisions on *Store.
type BackupSchedulesStore struct{ s *Store }

func NewBackupSchedules(s *Store) *BackupSchedulesStore { return &BackupSchedulesStore{s: s} }

// BackupSchedules on *Store satisfies storage.PlatformStore.BackupSchedules.
func (s *Store) BackupSchedules() storage.BackupScheduleStore { return NewBackupSchedules(s) }

func (b *BackupSchedulesStore) UpsertSchedule(ctx context.Context, sch *domain.BackupSchedule) error {
	_, err := b.s.db.ExecContext(ctx, `
		INSERT INTO backup_schedules (project_id, cron_spec, retention_days, enabled, updated_at)
		VALUES ($1, $2, $3, $4, NOW())
		ON CONFLICT (project_id) DO UPDATE SET
			cron_spec = EXCLUDED.cron_spec,
			retention_days = EXCLUDED.retention_days,
			enabled = EXCLUDED.enabled,
			updated_at = NOW()`,
		sch.ProjectID, sch.Cron, sch.RetentionDays, sch.Enabled)
	return err
}

func (b *BackupSchedulesStore) ListEnabledSchedules(ctx context.Context) ([]domain.BackupSchedule, error) {
	rows, err := b.s.db.QueryContext(ctx, `
		SELECT project_id, cron_spec, retention_days, enabled
		FROM backup_schedules WHERE enabled = TRUE`)
	if err != nil {
		return nil, fmt.Errorf("query backup_schedules: %w", err)
	}
	defer rows.Close()

	out := []domain.BackupSchedule{}
	for rows.Next() {
		var sch domain.BackupSchedule
		if err := rows.Scan(&sch.ProjectID, &sch.Cron, &sch.RetentionDays, &sch.Enabled); err != nil {
			return nil, err
		}
		out = append(out, sch)
	}
	return out, rows.Err()
}

func (b *BackupSchedulesStore) DeleteSchedule(ctx context.Context, projectID string) error {
	_, err := b.s.db.ExecContext(ctx, `DELETE FROM backup_schedules WHERE project_id = $1`, projectID)
	return err
}
