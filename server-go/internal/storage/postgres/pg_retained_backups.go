package postgres

import (
	"fmt"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/storage"
)

// RecordRetainedBackups stores when a deleted project's kept backups are
// purged, replacing an earlier record for the same project.
func (s *Store) RecordRetainedBackups(r storage.RetainedBackup) error {
	_, err := s.db.Exec(`
		INSERT INTO retained_backups (project_id, org_id, deployment_mode, deleted_at, purge_after)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (project_id) DO UPDATE SET
			org_id = EXCLUDED.org_id, deployment_mode = EXCLUDED.deployment_mode,
			deleted_at = EXCLUDED.deleted_at, purge_after = EXCLUDED.purge_after`,
		r.ProjectID, r.OrgID, string(r.DeploymentMode), r.DeletedAt, r.PurgeAfter)
	if err != nil {
		return fmt.Errorf("record retained backups for %s: %w", r.ProjectID, err)
	}
	return nil
}

// DueRetainedBackups lists the records whose purge date is at or before now.
func (s *Store) DueRetainedBackups(now time.Time) ([]storage.RetainedBackup, error) {
	rows, err := s.db.Query(`
		SELECT project_id, org_id, deployment_mode, deleted_at, purge_after
		FROM retained_backups WHERE purge_after <= $1 ORDER BY purge_after`, now)
	if err != nil {
		return nil, fmt.Errorf("list due retained backups: %w", err)
	}
	defer rows.Close()
	var due []storage.RetainedBackup
	for rows.Next() {
		var r storage.RetainedBackup
		var mode string
		if err := rows.Scan(&r.ProjectID, &r.OrgID, &mode, &r.DeletedAt, &r.PurgeAfter); err != nil {
			return nil, fmt.Errorf("scan retained backups: %w", err)
		}
		r.DeploymentMode = domain.DeploymentMode(mode)
		due = append(due, r)
	}
	return due, rows.Err()
}

// DeleteRetainedBackups removes the project's record once its backups are purged.
func (s *Store) DeleteRetainedBackups(projectID string) error {
	if _, err := s.db.Exec(`DELETE FROM retained_backups WHERE project_id = $1`, projectID); err != nil {
		return fmt.Errorf("delete retained backups record for %s: %w", projectID, err)
	}
	return nil
}
