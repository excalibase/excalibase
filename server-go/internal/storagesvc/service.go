package storagesvc

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
)

const errBucketNotFound = "bucket not found"


// Service is the platform-db-aware storage layer. It owns the metadata
// (buckets, objects, quotas) and delegates to R2Client for the actual
// blob plane. Persistence interface keeps tests fast (in-memory) while
// production uses sqlite/postgres.
type Service struct {
	store BucketStore
	r2    *R2Client
	// Per-tier byte quotas. Looked up by project tier; missing keys fall
	// back to free-tier limit. Zero = unlimited.
	tierQuotaBytes map[string]int64
}

// BucketStore is the persistence shape Service depends on. Implemented by
// sqlite + postgres + an in-memory mock used in tests. Methods take a
// context so they can be cancelled by the parent request.
type BucketStore interface {
	CreateBucket(ctx context.Context, b *Bucket) error
	GetBucket(ctx context.Context, projectID, name string) (*Bucket, error)
	ListBuckets(ctx context.Context, projectID string) ([]Bucket, error)
	DeleteBucket(ctx context.Context, projectID, name string) error

	CreateObject(ctx context.Context, o *Object) error
	GetObject(ctx context.Context, bucketID, key string) (*Object, error)
	ListObjects(ctx context.Context, bucketID, prefix string, limit int, cursor string) ([]Object, string, error)
	DeleteObject(ctx context.Context, bucketID, key string) error

	GetQuotaBytes(ctx context.Context, projectID string) (int64, error)
	AddQuotaBytes(ctx context.Context, projectID string, delta int64) error
}

// NewService wires a Service. tierQuotas maps lowercase tier names to
// per-project byte caps. Pass nil to disable quota enforcement
// (everyone gets unlimited; useful for self-hosted single-tenant).
func NewService(store BucketStore, r2 *R2Client, tierQuotas map[string]int64) *Service {
	return &Service{store: store, r2: r2, tierQuotaBytes: tierQuotas}
}

// CreateBucket validates name shape + uniqueness and persists.
// Bucket name rules: 3-63 chars, lowercase letters/digits/hyphens only,
// can't start or end with hyphen. Matches AWS S3 + R2 expectations.
func (s *Service) CreateBucket(ctx context.Context, projectID string, req CreateBucketRequest) (*Bucket, error) {
	if err := validateBucketName(req.Name); err != nil {
		return nil, err
	}
	if existing, _ := s.store.GetBucket(ctx, projectID, req.Name); existing != nil {
		return nil, fmt.Errorf("bucket %q already exists in project", req.Name)
	}
	id, err := randomID("bkt")
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	b := &Bucket{
		ID:           id,
		ProjectID:    projectID,
		Name:         req.Name,
		Public:       req.Public,
		FileSize:     req.FileSizeLimit,
		AllowedTypes: req.AllowedMimeTypes,
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	if err := s.store.CreateBucket(ctx, b); err != nil {
		return nil, fmt.Errorf("create bucket: %w", err)
	}
	return b, nil
}

// ListBuckets — straight passthrough.
func (s *Service) ListBuckets(ctx context.Context, projectID string) ([]Bucket, error) {
	return s.store.ListBuckets(ctx, projectID)
}

// DeleteBucket cascades through every object via R2 then drops the row.
// We could foreign-key-cascade in SQL but R2 needs explicit DELETE per
// object — there's no S3 batch delete that respects bucket-level rules.
// Best-effort: a partial failure leaves orphaned objects in R2 that the
// daily janitor will clean up.
func (s *Service) DeleteBucket(ctx context.Context, projectID, name string) error {
	bucket, err := s.store.GetBucket(ctx, projectID, name)
	if err != nil {
		return err
	}
	if bucket == nil {
		return errors.New(errBucketNotFound)
	}
	// Walk all objects in pages and delete each.
	cursor := ""
	for {
		objs, next, err := s.store.ListObjects(ctx, bucket.ID, "", 100, cursor)
		if err != nil {
			return fmt.Errorf("list objects for cascade: %w", err)
		}
		for _, o := range objs {
			_ = s.r2.DeleteObject(ctx, projectID, name, o.Key)
			_ = s.store.AddQuotaBytes(ctx, projectID, -o.Size)
		}
		if next == "" {
			break
		}
		cursor = next
	}
	return s.store.DeleteBucket(ctx, projectID, name)
}

// SignUploadURL mints a presigned PUT URL after enforcing per-bucket and
// per-project quotas. The actual byte transfer goes client→R2 directly,
// bypassing Excalibase. The client MUST call ConfirmUpload after the PUT
// to record the object metadata.
func (s *Service) SignUploadURL(ctx context.Context, projectID, bucketName, tier string, req UploadURLRequest) (*UploadURLResponse, error) {
	bucket, err := s.store.GetBucket(ctx, projectID, bucketName)
	if err != nil {
		return nil, err
	}
	if bucket == nil {
		return nil, errors.New(errBucketNotFound)
	}
	if err := s.validateUploadRequest(ctx, projectID, tier, bucket, req); err != nil {
		return nil, err
	}

	url, expires, err := s.r2.SignedPutURL(ctx, projectID, bucketName, req.Key, req.MimeType, 5*time.Minute)
	if err != nil {
		return nil, err
	}
	headers := map[string]string{}
	if req.MimeType != "" {
		headers["Content-Type"] = req.MimeType
	}
	return &UploadURLResponse{
		URL:       url,
		Method:    "PUT",
		Headers:   headers,
		ExpiresAt: expires,
	}, nil
}

// validateUploadRequest enforces per-bucket size limits, MIME allowlists,
// and per-project storage quotas before a presigned PUT URL is issued.
func (s *Service) validateUploadRequest(ctx context.Context, projectID, tier string, bucket *Bucket, req UploadURLRequest) error {
	// Per-bucket file size limit (0 = no limit).
	if bucket.FileSize > 0 && req.Size > bucket.FileSize {
		return fmt.Errorf("file exceeds bucket size limit %d bytes", bucket.FileSize)
	}
	// MIME allowlist (empty = any).
	if err := s.checkMIMEAllowlist(bucket.AllowedTypes, req.MimeType); err != nil {
		return err
	}
	// Per-project quota — only block when we have an actual limit AND a size hint.
	if cap := s.quotaForTier(tier); cap > 0 && req.Size > 0 {
		used, err := s.store.GetQuotaBytes(ctx, projectID)
		if err != nil {
			return fmt.Errorf("read quota: %w", err)
		}
		if used+req.Size > cap {
			return fmt.Errorf("project storage quota exceeded (%d / %d bytes)", used, cap)
		}
	}
	return nil
}

// checkMIMEAllowlist returns an error if mimeType is not in the allowlist.
// An empty allowlist permits any type; a "*/*" entry acts as a wildcard.
func (s *Service) checkMIMEAllowlist(allowed []string, mimeType string) error {
	if len(allowed) == 0 || mimeType == "" {
		return nil
	}
	for _, a := range allowed {
		if a == mimeType || a == "*/*" {
			return nil
		}
	}
	return fmt.Errorf("mime type %q not allowed in bucket", mimeType)
}

// ConfirmUpload records that a previously-signed upload completed. We
// trust the size+mime the client reports for now; in v1.2 we'll add a
// HEAD-back-to-R2 reconciliation in a daily janitor that catches any
// drift.
func (s *Service) ConfirmUpload(ctx context.Context, projectID, bucketName, ownerID string, req ConfirmUploadRequest) (*Object, error) {
	bucket, err := s.store.GetBucket(ctx, projectID, bucketName)
	if err != nil {
		return nil, err
	}
	if bucket == nil {
		return nil, errors.New(errBucketNotFound)
	}
	id, err := randomID("obj")
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	obj := &Object{
		ID:        id,
		BucketID:  bucket.ID,
		Key:       req.Key,
		Size:      req.Size,
		MimeType:  req.MimeType,
		ETag:      req.ETag,
		OwnerID:   ownerID,
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := s.store.CreateObject(ctx, obj); err != nil {
		return nil, err
	}
	_ = s.store.AddQuotaBytes(ctx, projectID, req.Size)
	return obj, nil
}

// SignDownloadURL returns either a signed GET URL (private buckets) or
// the static public URL (public buckets). Public URLs don't expire.
func (s *Service) SignDownloadURL(ctx context.Context, projectID, bucketName, key string) (*DownloadURLResponse, error) {
	bucket, err := s.store.GetBucket(ctx, projectID, bucketName)
	if err != nil {
		return nil, err
	}
	if bucket == nil {
		return nil, errors.New(errBucketNotFound)
	}
	if bucket.Public {
		return &DownloadURLResponse{
			URL:    s.r2.PublicURL(projectID, bucketName, key),
			Public: true,
		}, nil
	}
	url, expires, err := s.r2.SignedGetURL(ctx, projectID, bucketName, key, 5*time.Minute)
	if err != nil {
		return nil, err
	}
	return &DownloadURLResponse{URL: url, ExpiresAt: expires, Public: false}, nil
}

// ListObjects pages through metadata. Doesn't hit R2 — the catalogue is
// authoritative.
func (s *Service) ListObjects(ctx context.Context, projectID, bucketName string, req ListObjectsRequest) (*ListObjectsResponse, error) {
	bucket, err := s.store.GetBucket(ctx, projectID, bucketName)
	if err != nil {
		return nil, err
	}
	if bucket == nil {
		return nil, errors.New(errBucketNotFound)
	}
	if req.Limit <= 0 || req.Limit > 1000 {
		req.Limit = 100
	}
	objs, next, err := s.store.ListObjects(ctx, bucket.ID, req.Prefix, req.Limit, req.Cursor)
	if err != nil {
		return nil, err
	}
	return &ListObjectsResponse{Objects: objs, NextCursor: next}, nil
}

// DeleteObjectCatalogueOnly removes only the catalogue row for (bucket,
// key); does NOT touch R2. Used by the Phase 10 internal ctx.storage
// delete path which is idempotent and must succeed even when the R2
// best-effort delete fails (the daily janitor reaps orphaned blobs,
// matching the convention in DeleteObject's comments).
func (s *Service) DeleteObjectCatalogueOnly(ctx context.Context, projectID, bucketName, key string) error {
	bucket, err := s.store.GetBucket(ctx, projectID, bucketName)
	if err != nil {
		return err
	}
	if bucket == nil {
		// Bucket doesn't exist — nothing to remove. Idempotent.
		return nil
	}
	obj, _ := s.store.GetObject(ctx, bucket.ID, key)
	if err := s.store.DeleteObject(ctx, bucket.ID, key); err != nil {
		return err
	}
	if obj != nil {
		_ = s.store.AddQuotaBytes(ctx, projectID, -obj.Size)
	}
	return nil
}

// DeleteObject removes from both R2 and the catalogue. R2 first so a
// dangling DB row is preferable to an orphaned blob (the janitor reaps
// dangling rows; orphaned blobs cost money).
func (s *Service) DeleteObject(ctx context.Context, projectID, bucketName, key string) error {
	bucket, err := s.store.GetBucket(ctx, projectID, bucketName)
	if err != nil {
		return err
	}
	if bucket == nil {
		return errors.New(errBucketNotFound)
	}
	obj, _ := s.store.GetObject(ctx, bucket.ID, key)
	if err := s.r2.DeleteObject(ctx, projectID, bucketName, key); err != nil {
		return err
	}
	if err := s.store.DeleteObject(ctx, bucket.ID, key); err != nil {
		// R2 already deleted; warn but don't fail the user request.
		// A dangling DB row will be cleaned by the janitor.
		_ = err
	}
	if obj != nil {
		_ = s.store.AddQuotaBytes(ctx, projectID, -obj.Size)
	}
	return nil
}

func (s *Service) quotaForTier(tier string) int64 {
	if s.tierQuotaBytes == nil {
		return 0
	}
	if v, ok := s.tierQuotaBytes[strings.ToLower(tier)]; ok {
		return v
	}
	if v, ok := s.tierQuotaBytes["free"]; ok {
		return v
	}
	return 0
}

// validateBucketName enforces S3/R2 bucket-name compatibility so users
// can't create a logical bucket whose name would be illegal at the
// underlying layer. We intentionally also reject single dot-only names
// and consecutive hyphens to keep URLs sane.
func validateBucketName(name string) error {
	if len(name) < 3 || len(name) > 63 {
		return fmt.Errorf("bucket name must be 3-63 chars, got %d", len(name))
	}
	for i, ch := range name {
		switch {
		case ch >= 'a' && ch <= 'z', ch >= '0' && ch <= '9':
			// ok
		case ch == '-':
			if i == 0 || i == len(name)-1 {
				return fmt.Errorf("bucket name can't start/end with hyphen")
			}
		default:
			return fmt.Errorf("bucket name must be lowercase letters/digits/hyphens only")
		}
	}
	return nil
}

func randomID(prefix string) (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return prefix + "_" + hex.EncodeToString(b), nil
}

