package sqlite

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/storage"
)

// BackupRecordsStore adapts Store to the storage.BackupRecordStore
// interface. See pg_backup_records.go for the rationale on the
// wrapper indirection (Store.Save already takes *DatabaseInstance).
type BackupRecordsStore struct{ s *Store }

func NewBackupRecords(s *Store) *BackupRecordsStore { return &BackupRecordsStore{s: s} }

// BackupRecords on *Store satisfies storage.PlatformStore.BackupRecords.
func (s *Store) BackupRecords() storage.BackupRecordStore { return NewBackupRecords(s) }

func (b *BackupRecordsStore) Save(ctx context.Context, r *domain.BackupRecord) error {
	return b.s.saveBackupRecord(ctx, r)
}

func (b *BackupRecordsStore) ListByProject(ctx context.Context, projectID string) ([]domain.BackupRecord, error) {
	return b.s.listBackupsByProject(ctx, projectID)
}

func (b *BackupRecordsStore) UpdateStatus(ctx context.Context, id, status string) error {
	return b.s.updateBackupStatus(ctx, id, status)
}

func (s *Store) saveBackupRecord(ctx context.Context, r *domain.BackupRecord) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO backup_records (id, project_id, timestamp, type, status)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			timestamp = excluded.timestamp,
			type = excluded.type,
			status = excluded.status`,
		r.ID, r.ProjectID, r.Timestamp, r.Type, r.Status,
	)
	return err
}

func (s *Store) listBackupsByProject(ctx context.Context, projectID string) ([]domain.BackupRecord, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, project_id, timestamp, type, status
		FROM backup_records
		WHERE project_id = ?
		ORDER BY timestamp DESC`, projectID)
	if err != nil {
		return nil, fmt.Errorf("query backup_records: %w", err)
	}
	defer rows.Close()

	out := []domain.BackupRecord{}
	for rows.Next() {
		var r domain.BackupRecord
		var ts sql.NullString
		if err := rows.Scan(&r.ID, &r.ProjectID, &ts, &r.Type, &r.Status); err != nil {
			return nil, err
		}
		if ts.Valid {
			r.Timestamp = ts.String
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *Store) updateBackupStatus(ctx context.Context, id, status string) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE backup_records SET status = ? WHERE id = ?`, status, id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("backup record %s not found", id)
	}
	return nil
}
