package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/excalibase/provisioning-poc/internal/storagesvc"
)

// --- BucketStore (storagesvc.BucketStore) ---
//
// Postgres mirror of sqlite_storage.go. Uses native bool + JSONB columns
// where SQLite uses INTEGER + TEXT, but the contract is identical.

func (s *Store) CreateBucket(ctx context.Context, b *storagesvc.Bucket) error {
	allowed, _ := json.Marshal(b.AllowedTypes)
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO storage_buckets (id, project_id, name, public, status, file_size_limit, allowed_mime_types, created_at, updated_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7::jsonb, $8, $9)`,
		b.ID, b.ProjectID, b.Name, b.Public, bucketStatusOrDefault(b.Status), b.FileSize, string(allowed),
		b.CreatedAt, b.UpdatedAt)
	if err != nil {
		if strings.Contains(err.Error(), "duplicate") {
			return fmt.Errorf("bucket already exists")
		}
		return err
	}
	return nil
}

func (s *Store) GetBucket(ctx context.Context, projectID, name string) (*storagesvc.Bucket, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, project_id, name, public, status, file_size_limit, allowed_mime_types, created_at, updated_at
		 FROM storage_buckets WHERE project_id = $1 AND name = $2`, projectID, name)
	return scanBucket(row)
}

func (s *Store) ListBuckets(ctx context.Context, projectID string) ([]storagesvc.Bucket, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, project_id, name, public, status, file_size_limit, allowed_mime_types, created_at, updated_at
		 FROM storage_buckets WHERE project_id = $1 ORDER BY name`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []storagesvc.Bucket{}
	for rows.Next() {
		b, err := scanBucket(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *b)
	}
	return out, rows.Err()
}

// ListAllBuckets spans every project, for the storage reaper's sweep.
func (s *Store) ListAllBuckets(ctx context.Context) ([]storagesvc.Bucket, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, project_id, name, public, status, file_size_limit, allowed_mime_types, created_at, updated_at
		 FROM storage_buckets ORDER BY project_id, name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []storagesvc.Bucket{}
	for rows.Next() {
		b, err := scanBucket(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *b)
	}
	return out, rows.Err()
}

func (s *Store) DeleteBucket(ctx context.Context, projectID, name string) error {
	res, err := s.db.ExecContext(ctx,
		`DELETE FROM storage_buckets WHERE project_id = $1 AND name = $2`, projectID, name)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("bucket not found")
	}
	return nil
}

// SetBucketStatus moves a bucket through its lifecycle. A status write that
// matches no row is an error: the caller believes the bucket exists.
func (s *Store) SetBucketStatus(ctx context.Context, projectID, name, status string) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE storage_buckets SET status = $3, updated_at = NOW()
		 WHERE project_id = $1 AND name = $2`, projectID, name, status)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return fmt.Errorf("bucket not found")
	}
	return nil
}

// bucketStatusOrDefault keeps rows written before the status column existed
// readable as active.
func bucketStatusOrDefault(status string) string {
	if status == "" {
		return storagesvc.BucketStatusActive
	}
	return status
}

func (s *Store) CreateObject(ctx context.Context, o *storagesvc.Object) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO storage_objects (id, bucket_id, key, size, mime_type, etag, owner_id, created_at, updated_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		 ON CONFLICT (bucket_id, key) DO UPDATE SET
		   size = EXCLUDED.size, mime_type = EXCLUDED.mime_type, etag = EXCLUDED.etag,
		   updated_at = EXCLUDED.updated_at`,
		o.ID, o.BucketID, o.Key, o.Size, o.MimeType, o.ETag, o.OwnerID,
		o.CreatedAt, o.UpdatedAt)
	return err
}

func (s *Store) ListObjects(ctx context.Context, bucketID, prefix string, limit int, cursor string) ([]storagesvc.Object, string, error) {
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	q := `SELECT id, bucket_id, key, size, mime_type, etag, owner_id, created_at, updated_at
	      FROM storage_objects WHERE bucket_id = $1`
	args := []interface{}{bucketID}
	idx := 2
	if prefix != "" {
		// starts_with, not LIKE: a user prefix is a literal key prefix, and
		// "%" or "_" in it must not widen the match to a neighbour's objects.
		q += fmt.Sprintf(" AND starts_with(key, $%d)", idx)
		args = append(args, prefix)
		idx++
	}
	if cursor != "" {
		q += fmt.Sprintf(" AND key > $%d", idx)
		args = append(args, cursor)
		idx++
	}
	q += fmt.Sprintf(" ORDER BY key LIMIT $%d", idx)
	args = append(args, limit+1)
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()
	out := []storagesvc.Object{}
	for rows.Next() {
		o, err := scanObject(rows)
		if err != nil {
			return nil, "", err
		}
		out = append(out, *o)
	}
	next := ""
	if len(out) > limit {
		next = out[limit-1].Key
		out = out[:limit]
	}
	return out, next, nil
}

// DeleteObjectAndReleaseQuota drops the row and releases exactly the bytes
// that row was charged for, in one transaction. Splitting the two would let
// the release be lost: the retry finds no row, so no size, and the project
// keeps paying for bytes that are gone. The size comes from the deleted row
// itself, never from a caller.
//
// The quota row is taken FIRST, before the object row — the same order
// RecordObjectWithinQuota uses. Taking them in opposite orders is what makes
// a concurrent confirm and delete of one key deadlock: each holds what the
// other waits for, and Postgres aborts one of them.
func (s *Store) DeleteObjectAndReleaseQuota(ctx context.Context, projectID, bucketID, key string) (bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("begin delete object: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // rollback after commit is a no-op

	if _, err := lockProjectQuota(ctx, tx, projectID); err != nil {
		return false, err
	}

	var size int64
	err = tx.QueryRowContext(ctx,
		`DELETE FROM storage_objects WHERE bucket_id = $1 AND key = $2 RETURNING size`,
		bucketID, key).Scan(&size)
	if errors.Is(err, sql.ErrNoRows) {
		// Nothing to remove and nothing to release.
		return false, tx.Commit()
	}
	if err != nil {
		return false, fmt.Errorf("delete object row: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE storage_quota SET bytes_used = GREATEST(0, bytes_used - $2), updated_at = NOW()
		 WHERE project_id = $1`, projectID, size); err != nil {
		return false, fmt.Errorf("release quota: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("commit delete object: %w", err)
	}
	return true, nil
}

// lockProjectQuota takes the project's quota row for the rest of the
// transaction and returns the usage it holds, creating the row when the
// project has never stored anything. Every path that touches both the quota
// and an object row starts here, so the two are always taken in one order.
func lockProjectQuota(ctx context.Context, tx *sql.Tx, projectID string) (int64, error) {
	var used int64
	err := tx.QueryRowContext(ctx,
		`INSERT INTO storage_quota (project_id, bytes_used, updated_at)
		 VALUES ($1, 0, NOW())
		 ON CONFLICT (project_id) DO UPDATE SET project_id = EXCLUDED.project_id
		 RETURNING bytes_used`, projectID).Scan(&used)
	if err != nil {
		return 0, fmt.Errorf("lock quota: %w", err)
	}
	return used, nil
}

// RecordObjectWithinQuota writes an object row and moves the project's usage
// by the difference between what it now holds and what the row it replaces
// held, inside one transaction that refuses to cross capBytes.
//
// The quota row is taken first and held for the duration, so two confirms for
// the same project serialise on it: each sees the other's spend rather than
// both reading a total that was already stale. A repeat of the same confirm
// moves the usage by zero; an overwrite moves it by the difference, in either
// direction. Returns false when the charge would exceed the cap, having
// written nothing.
func (s *Store) RecordObjectWithinQuota(ctx context.Context, projectID string, o *storagesvc.Object, capBytes int64) (bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("begin record object: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // rollback after commit is a no-op

	used, err := lockProjectQuota(ctx, tx, projectID)
	if err != nil {
		return false, err
	}

	var previous int64
	err = tx.QueryRowContext(ctx,
		`SELECT COALESCE((SELECT size FROM storage_objects WHERE bucket_id = $1 AND key = $2), 0)`,
		o.BucketID, o.Key).Scan(&previous)
	if err != nil {
		return false, fmt.Errorf("read previous size: %w", err)
	}

	delta := o.Size - previous
	if capBytes > 0 && used+delta > capBytes {
		return false, nil
	}

	if _, err := tx.ExecContext(ctx,
		`INSERT INTO storage_objects (id, bucket_id, key, size, mime_type, etag, owner_id, created_at, updated_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		 ON CONFLICT (bucket_id, key) DO UPDATE SET
		   size = EXCLUDED.size, mime_type = EXCLUDED.mime_type, etag = EXCLUDED.etag,
		   updated_at = EXCLUDED.updated_at`,
		o.ID, o.BucketID, o.Key, o.Size, o.MimeType, o.ETag, o.OwnerID,
		o.CreatedAt, o.UpdatedAt); err != nil {
		return false, fmt.Errorf("write object row: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE storage_quota SET bytes_used = GREATEST(0, bytes_used + $2), updated_at = NOW()
		 WHERE project_id = $1`, projectID, delta); err != nil {
		return false, fmt.Errorf("charge quota: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("commit record object: %w", err)
	}
	return true, nil
}

func (s *Store) GetObject(ctx context.Context, bucketID, key string) (*storagesvc.Object, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, bucket_id, key, size, mime_type, etag, owner_id, created_at, updated_at
		 FROM storage_objects WHERE bucket_id = $1 AND key = $2`, bucketID, key)
	return scanObject(row)
}

func (s *Store) GetQuotaBytes(ctx context.Context, projectID string) (int64, error) {
	var n int64
	err := s.db.QueryRowContext(ctx,
		`SELECT COALESCE(bytes_used, 0) FROM storage_quota WHERE project_id = $1`, projectID).Scan(&n)
	if err == sql.ErrNoRows {
		return 0, nil
	}
	return n, err
}

func (s *Store) AddQuotaBytes(ctx context.Context, projectID string, delta int64) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO storage_quota (project_id, bytes_used, updated_at)
		 VALUES ($1, $2, NOW())
		 ON CONFLICT (project_id) DO UPDATE SET
		   bytes_used = GREATEST(0, storage_quota.bytes_used + EXCLUDED.bytes_used),
		   updated_at = EXCLUDED.updated_at`,
		projectID, delta)
	return err
}

// --- scan helpers ---

type scannable interface {
	Scan(dest ...interface{}) error
}

func scanBucket(r scannable) (*storagesvc.Bucket, error) {
	var b storagesvc.Bucket
	var allowedRaw sql.NullString
	var fileSize sql.NullInt64
	var created, updated time.Time
	var status sql.NullString
	err := r.Scan(&b.ID, &b.ProjectID, &b.Name, &b.Public, &status, &fileSize, &allowedRaw, &created, &updated)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	b.Status = bucketStatusOrDefault(status.String)
	if fileSize.Valid {
		b.FileSize = fileSize.Int64
	}
	if allowedRaw.Valid && allowedRaw.String != "" && allowedRaw.String != "null" {
		_ = json.Unmarshal([]byte(allowedRaw.String), &b.AllowedTypes)
	}
	b.CreatedAt = created
	b.UpdatedAt = updated
	return &b, nil
}

func scanObject(r scannable) (*storagesvc.Object, error) {
	var o storagesvc.Object
	var mime, etag, owner sql.NullString
	var created, updated time.Time
	err := r.Scan(&o.ID, &o.BucketID, &o.Key, &o.Size, &mime, &etag, &owner, &created, &updated)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if mime.Valid {
		o.MimeType = mime.String
	}
	if etag.Valid {
		o.ETag = etag.String
	}
	if owner.Valid {
		o.OwnerID = owner.String
	}
	o.CreatedAt = created
	o.UpdatedAt = updated
	return &o, nil
}
