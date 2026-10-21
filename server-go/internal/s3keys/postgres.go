package s3keys

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/lib/pq"
)

// keyLockSpace keeps the per-project key count apart from other advisory locks.
const keyLockSpace = 630

const keyColumns = `id, project_id, name, access_key_id, buckets, can_read, can_write, can_delete,
	created_by, created_at, expires_at, last_used_at`

// PostgresStore keeps key rows in the platform database.
type PostgresStore struct{ db *sql.DB }

func NewPostgresStore(db *sql.DB) *PostgresStore { return &PostgresStore{db: db} }

type rowScanner interface{ Scan(dest ...any) error }

func scanKey(row rowScanner) (*Key, error) {
	key := &Key{}
	var buckets pq.StringArray
	var expires, lastUsed sql.NullTime
	if err := row.Scan(&key.ID, &key.ProjectID, &key.Name, &key.AccessKeyID, &buckets, &key.Read, &key.Write,
		&key.Delete, &key.CreatedBy, &key.CreatedAt, &expires, &lastUsed); err != nil {
		return nil, err
	}
	key.Buckets = []string(buckets)
	if key.Buckets == nil {
		key.Buckets = []string{}
	}
	key.CreatedAt = key.CreatedAt.UTC()
	if expires.Valid {
		at := expires.Time.UTC()
		key.ExpiresAt = &at
	}
	if lastUsed.Valid {
		at := lastUsed.Time.UTC()
		key.LastUsedAt = &at
	}
	return key, nil
}

// Create counts the project's keys under a per-project lock, so two creates
// cannot both take the last slot.
func (s *PostgresStore) Create(ctx context.Context, key *Key, maxPerProject int) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin S3 key create: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock($1, hashtext($2))`, keyLockSpace, key.ProjectID); err != nil {
		return fmt.Errorf("lock project S3 key count: %w", err)
	}
	var held int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM storage_s3_keys WHERE project_id = $1`, key.ProjectID).Scan(&held); err != nil {
		return fmt.Errorf("count S3 keys: %w", err)
	}
	if held >= maxPerProject {
		return ErrLimitReached
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO storage_s3_keys (id, project_id, name, access_key_id, buckets,
		can_read, can_write, can_delete, created_by, created_at, expires_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)`,
		key.ID, key.ProjectID, key.Name, key.AccessKeyID, pq.Array(key.Buckets), key.Read, key.Write, key.Delete,
		key.CreatedBy, key.CreatedAt, key.ExpiresAt); err != nil {
		return fmt.Errorf("create S3 key: %w", err)
	}
	return tx.Commit()
}

func (s *PostgresStore) List(ctx context.Context, projectID string) ([]*Key, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+keyColumns+` FROM storage_s3_keys
		WHERE project_id = $1 ORDER BY created_at, id`, projectID)
	if err != nil {
		return nil, fmt.Errorf("list S3 keys: %w", err)
	}
	defer rows.Close()
	out := []*Key{}
	for rows.Next() {
		key, err := scanKey(rows)
		if err != nil {
			return nil, fmt.Errorf("scan S3 key: %w", err)
		}
		out = append(out, key)
	}
	return out, rows.Err()
}

func (s *PostgresStore) FindByAccessKeyID(ctx context.Context, accessKeyID string) (*Key, error) {
	key, err := scanKey(s.db.QueryRowContext(ctx, `SELECT `+keyColumns+` FROM storage_s3_keys WHERE access_key_id = $1`, accessKeyID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("find S3 key: %w", err)
	}
	return key, nil
}

func (s *PostgresStore) Delete(ctx context.Context, projectID, id string) (*Key, error) {
	key, err := scanKey(s.db.QueryRowContext(ctx, `DELETE FROM storage_s3_keys WHERE project_id = $1 AND id = $2
		RETURNING `+keyColumns, projectID, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("revoke S3 key: %w", err)
	}
	return key, nil
}

func (s *PostgresStore) DeleteProject(ctx context.Context, projectID string) (int, error) {
	result, err := s.db.ExecContext(ctx, `DELETE FROM storage_s3_keys WHERE project_id = $1`, projectID)
	if err != nil {
		return 0, fmt.Errorf("remove the project's S3 keys: %w", err)
	}
	removed, err := result.RowsAffected()
	return int(removed), err
}

func (s *PostgresStore) TouchLastUsed(ctx context.Context, id string, at time.Time) error {
	_, err := s.db.ExecContext(ctx, `UPDATE storage_s3_keys SET last_used_at = $2 WHERE id = $1`, id, at)
	return err
}
