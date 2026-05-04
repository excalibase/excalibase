package postgres

import (
	"context"
	"fmt"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/storage"
)

// BackupRecordsStore adapts Store to the storage.BackupRecordStore
// interface. Done as a wrapper so the existing Store.Save (which
// takes a *DatabaseInstance) doesn't collide with the
// (ctx, *BackupRecord) signature on the interface.
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

// saveBackupRecord upserts a backup_records row.
func (s *Store) saveBackupRecord(ctx context.Context, r *domain.BackupRecord) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO backup_records (id, project_id, timestamp, type, status)
		VALUES ($1, $2, $3::timestamptz, $4, $5)
		ON CONFLICT (id) DO UPDATE SET
			timestamp = EXCLUDED.timestamp,
			type = EXCLUDED.type,
			status = EXCLUDED.status`,
		r.ID, r.ProjectID, nullTimestamp(r.Timestamp), r.Type, r.Status,
	)
	return err
}

func (s *Store) listBackupsByProject(ctx context.Context, projectID string) ([]domain.BackupRecord, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, project_id, timestamp, type, status
		FROM backup_records
		WHERE project_id = $1
		ORDER BY timestamp DESC`, projectID)
	if err != nil {
		return nil, fmt.Errorf("query backup_records: %w", err)
	}
	defer rows.Close()

	out := []domain.BackupRecord{}
	for rows.Next() {
		r, err := scanBackupRecord(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *Store) updateBackupStatus(ctx context.Context, id, status string) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE backup_records SET status = $1 WHERE id = $2`, status, id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("backup record %s not found", id)
	}
	return nil
}

func scanBackupRecord(s scannable) (domain.BackupRecord, error) {
	var r domain.BackupRecord
	var ts interface{}
	if err := s.Scan(&r.ID, &r.ProjectID, &ts, &r.Type, &r.Status); err != nil {
		return r, err
	}
	r.Timestamp = formatTimestamp(ts)
	return r, nil
}

// nullTimestamp returns nil when the input is empty so Postgres
// stores NULL instead of failing on an empty string cast.
func nullTimestamp(ts string) interface{} {
	if ts == "" {
		return nil
	}
	return ts
}

// formatTimestamp normalizes a TIMESTAMPTZ scan back to RFC3339,
// which is the format the rest of the platform serializes to JSON.
func formatTimestamp(v interface{}) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	default:
		// time.Time and other Stringer implementations.
		if s, ok := v.(fmt.Stringer); ok {
			return s.String()
		}
		return ""
	}
}
