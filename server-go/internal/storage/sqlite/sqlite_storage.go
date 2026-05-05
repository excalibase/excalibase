package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/excalibase/provisioning-poc/internal/storagesvc"
)

// --- BucketStore (storagesvc.BucketStore) ---
//
// Adds storage_buckets / storage_objects / storage_quota CRUD on top of
// the existing sqlite Store. Methods mirror the storagesvc.BucketStore
// interface so the service layer can be store-agnostic.

func (s *Store) CreateBucket(ctx context.Context, b *storagesvc.Bucket) error {
	allowed, _ := json.Marshal(b.AllowedTypes)
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO storage_buckets (id, project_id, name, public, file_size_limit, allowed_mime_types, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		b.ID, b.ProjectID, b.Name, boolToInt(&b.Public), b.FileSize, string(allowed),
		b.CreatedAt.Format(time.RFC3339), b.UpdatedAt.Format(time.RFC3339))
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return fmt.Errorf("bucket already exists")
		}
		return err
	}
	return nil
}

func (s *Store) GetBucket(ctx context.Context, projectID, name string) (*storagesvc.Bucket, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, project_id, name, public, file_size_limit, allowed_mime_types, created_at, updated_at
		 FROM storage_buckets WHERE project_id = ? AND name = ?`, projectID, name)
	return scanBucket(row)
}

func (s *Store) ListBuckets(ctx context.Context, projectID string) ([]storagesvc.Bucket, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, project_id, name, public, file_size_limit, allowed_mime_types, created_at, updated_at
		 FROM storage_buckets WHERE project_id = ? ORDER BY name`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []storagesvc.Bucket{}
	for rows.Next() {
		b, err := scanBucketRows(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *b)
	}
	return out, rows.Err()
}

func (s *Store) DeleteBucket(ctx context.Context, projectID, name string) error {
	res, err := s.db.ExecContext(ctx,
		`DELETE FROM storage_buckets WHERE project_id = ? AND name = ?`, projectID, name)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("bucket not found")
	}
	return nil
}

func (s *Store) CreateObject(ctx context.Context, o *storagesvc.Object) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO storage_objects (id, bucket_id, key, size, mime_type, etag, owner_id, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(bucket_id, key) DO UPDATE SET
		   size = excluded.size, mime_type = excluded.mime_type, etag = excluded.etag,
		   updated_at = excluded.updated_at`,
		o.ID, o.BucketID, o.Key, o.Size, o.MimeType, o.ETag, o.OwnerID,
		o.CreatedAt.Format(time.RFC3339), o.UpdatedAt.Format(time.RFC3339))
	return err
}

func (s *Store) GetObject(ctx context.Context, bucketID, key string) (*storagesvc.Object, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, bucket_id, key, size, mime_type, etag, owner_id, created_at, updated_at
		 FROM storage_objects WHERE bucket_id = ? AND key = ?`, bucketID, key)
	return scanObject(row)
}

// ListObjects pages on (bucket_id, key) using the cursor as the last-seen
// key. Empty cursor starts from the beginning. Returns "" when there are
// no more rows.
func (s *Store) ListObjects(ctx context.Context, bucketID, prefix string, limit int, cursor string) ([]storagesvc.Object, string, error) {
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	q := `SELECT id, bucket_id, key, size, mime_type, etag, owner_id, created_at, updated_at
	      FROM storage_objects WHERE bucket_id = ?`
	args := []interface{}{bucketID}
	if prefix != "" {
		q += ` AND key LIKE ?`
		args = append(args, prefix+"%")
	}
	if cursor != "" {
		q += ` AND key > ?`
		args = append(args, cursor)
	}
	q += ` ORDER BY key LIMIT ?`
	args = append(args, limit+1) // ask for one extra to know if there's a next page

	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()
	out := []storagesvc.Object{}
	for rows.Next() {
		o, err := scanObjectRows(rows)
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

func (s *Store) DeleteObject(ctx context.Context, bucketID, key string) error {
	_, err := s.db.ExecContext(ctx,
		`DELETE FROM storage_objects WHERE bucket_id = ? AND key = ?`, bucketID, key)
	return err
}

func (s *Store) GetQuotaBytes(ctx context.Context, projectID string) (int64, error) {
	var n int64
	err := s.db.QueryRowContext(ctx,
		`SELECT COALESCE(bytes_used, 0) FROM storage_quota WHERE project_id = ?`, projectID).Scan(&n)
	if err == sql.ErrNoRows {
		return 0, nil
	}
	return n, err
}

func (s *Store) AddQuotaBytes(ctx context.Context, projectID string, delta int64) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO storage_quota (project_id, bytes_used, updated_at)
		 VALUES (?, ?, ?)
		 ON CONFLICT(project_id) DO UPDATE SET
		   bytes_used = MAX(0, bytes_used + excluded.bytes_used),
		   updated_at = excluded.updated_at`,
		projectID, delta, time.Now().UTC().Format(time.RFC3339))
	return err
}

// --- scan helpers ---
// rowScanner is shared with sqlite_tokens.go (declared once there).

func scanBucket(r rowScanner) (*storagesvc.Bucket, error) {
	var b storagesvc.Bucket
	var public int
	var allowed sql.NullString
	var fileSize sql.NullInt64
	var created, updated string
	err := r.Scan(&b.ID, &b.ProjectID, &b.Name, &public, &fileSize, &allowed, &created, &updated)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	b.Public = public != 0
	if fileSize.Valid {
		b.FileSize = fileSize.Int64
	}
	if allowed.Valid && allowed.String != "" && allowed.String != "null" {
		_ = json.Unmarshal([]byte(allowed.String), &b.AllowedTypes)
	}
	b.CreatedAt, _ = time.Parse(time.RFC3339, created)
	b.UpdatedAt, _ = time.Parse(time.RFC3339, updated)
	return &b, nil
}

func scanBucketRows(r *sql.Rows) (*storagesvc.Bucket, error) {
	return scanBucket(r)
}

func scanObject(r rowScanner) (*storagesvc.Object, error) {
	var o storagesvc.Object
	var mime, etag, owner sql.NullString
	var created, updated string
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
	o.CreatedAt, _ = time.Parse(time.RFC3339, created)
	o.UpdatedAt, _ = time.Parse(time.RFC3339, updated)
	return &o, nil
}

func scanObjectRows(r *sql.Rows) (*storagesvc.Object, error) {
	return scanObject(r)
}

// boolToInt is declared in sqlite.go.
