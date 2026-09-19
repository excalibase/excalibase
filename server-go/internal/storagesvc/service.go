package storagesvc

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// deleteBucketPageSize bounds one page of the cascade walk.
const deleteBucketPageSize = 100

// uploadURLTTL bounds how long a signed PUT stays usable. It is also the
// floor for how long an unconfirmed object is left alone before the reaper
// may collect it.
const uploadURLTTL = 5 * time.Minute

// Service is the platform-db-aware storage layer. It owns the metadata
// (buckets, objects, quotas) and delegates to an ObjectStore for the actual
// blob plane. Persistence interface keeps tests fast (in-memory) while
// production uses sqlite/postgres.
type Service struct {
	store   BucketStore
	objects ObjectStore
	// tusR2 is the concrete client the resumable-upload composer needs; it
	// reaches into the S3 SDK types the ObjectStore interface deliberately
	// hides. Nil when the service runs on a non-R2 blob plane.
	tusR2 *R2Client
	// Per-tier byte quotas. Looked up by project tier; missing keys fall
	// back to free-tier limit. Zero = unlimited.
	tierQuotaBytes map[string]int64
	// now is the clock the confirm window is measured against; injectable
	// so the boundary can be tested without waiting an hour.
	now func() time.Time
}

// BucketStore is the persistence shape Service depends on. Implemented by
// sqlite + postgres + an in-memory mock used in tests. Methods take a
// context so they can be cancelled by the parent request.
type BucketStore interface {
	CreateBucket(ctx context.Context, b *Bucket) error
	GetBucket(ctx context.Context, projectID, name string) (*Bucket, error)
	ListBuckets(ctx context.Context, projectID string) ([]Bucket, error)
	// ListAllBuckets spans every project. Only the reaper uses it: finding
	// abandoned uploads means walking the whole blob plane, not one tenant.
	ListAllBuckets(ctx context.Context) ([]Bucket, error)
	DeleteBucket(ctx context.Context, projectID, name string) error
	// SetBucketStatus records where a bucket is in its lifecycle. Used to
	// mark a bucket deleting before its bytes go, so a crash mid-cascade is
	// visible rather than silent.
	SetBucketStatus(ctx context.Context, projectID, name, status string) error

	CreateObject(ctx context.Context, o *Object) error
	// RecordObjectWithinQuota writes (or replaces) an object row and moves
	// the project's usage by the difference between the new size and the row
	// it replaced, in one transaction that refuses to cross capBytes (0 =
	// unlimited). Returns false when the charge would exceed the cap and
	// nothing was written — the check and the spend are the same write, so
	// concurrent confirms cannot each pass a read that was already stale.
	RecordObjectWithinQuota(ctx context.Context, projectID string, o *Object, capBytes int64) (recorded bool, err error)
	GetObject(ctx context.Context, bucketID, key string) (*Object, error)
	ListObjects(ctx context.Context, bucketID, prefix string, limit int, cursor string) ([]Object, string, error)
	// DeleteObjectAndReleaseQuota removes an object's row and releases the
	// bytes it was charged for, in one transaction: a release that could be
	// lost separately would charge the project forever, because the retry
	// finds no row and so no size. Returns false when there was no row.
	DeleteObjectAndReleaseQuota(ctx context.Context, projectID, bucketID, key string) (removed bool, err error)

	GetQuotaBytes(ctx context.Context, projectID string) (int64, error)
	AddQuotaBytes(ctx context.Context, projectID string, delta int64) error
}

// NewService wires a Service. tierQuotas maps lowercase tier names to
// per-project byte caps. Pass nil to disable quota enforcement
// (everyone gets unlimited; useful for self-hosted single-tenant).
func NewService(store BucketStore, r2 *R2Client, tierQuotas map[string]int64) *Service {
	s := &Service{store: store, tusR2: r2, tierQuotaBytes: tierQuotas, now: time.Now}
	if r2 != nil {
		s.objects = r2
	}
	return s
}

// NewServiceWithObjectStore wires an arbitrary blob plane. Resumable (tus)
// uploads stay disabled: the composer needs the concrete S3 client.
func NewServiceWithObjectStore(store BucketStore, objects ObjectStore, tierQuotas map[string]int64) *Service {
	return &Service{store: store, objects: objects, tierQuotaBytes: tierQuotas, now: time.Now}
}

// CreateBucket validates name shape + uniqueness and persists.
// Bucket name rules: 3-63 chars, lowercase letters/digits/hyphens only,
// can't start or end with hyphen. Matches AWS S3 + R2 expectations.
func (s *Service) CreateBucket(ctx context.Context, projectID string, req CreateBucketRequest) (*Bucket, error) {
	if err := validateBucketName(req.Name); err != nil {
		return nil, err
	}
	existing, err := s.store.GetBucket(ctx, projectID, req.Name)
	if err != nil {
		return nil, fmt.Errorf("check existing bucket: %w", err)
	}
	if existing != nil {
		return nil, ErrBucketExists
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
		Status:       BucketStatusActive,
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

// DeleteBucket removes a bucket and everything in it. The ordering is the
// point of this function:
//
//	mark deleting → delete the bytes → verify the prefix is empty → drop metadata
//
// Doing metadata first would leave bytes nobody holds a record of: nothing
// could ever find, delete or bill them. Doing it in this order instead risks
// catalogue rows that outlive their bytes — which is why the bucket is marked
// deleting up front, so that state is explicit rather than silent, and a
// repeated DELETE finishes the job. Any failure leaves the bucket record in
// place so that retry is possible; already-missing objects count as deleted.
func (s *Service) DeleteBucket(ctx context.Context, projectID, name string) error {
	bucket, err := s.store.GetBucket(ctx, projectID, name)
	if err != nil {
		return fmt.Errorf("load bucket: %w", err)
	}
	if bucket == nil {
		return ErrBucketNotFound
	}
	if s.objects == nil {
		return errObjectStoreUnset
	}
	if bucket.Status != BucketStatusDeleting {
		if err := s.store.SetBucketStatus(ctx, projectID, name, BucketStatusDeleting); err != nil {
			return fmt.Errorf("mark bucket deleting: %w", err)
		}
	}
	if err := s.purgeBucketObjects(ctx, projectID, bucket.ID); err != nil {
		return err
	}
	// Whatever is left under the prefix belongs to this bucket and to nothing
	// else — ids are never reused — so the delete clears it rather than
	// refusing until something else does. These are uploads that were never
	// confirmed: bytes with no row, which no other path can even name.
	if err := s.purgeUncataloguedObjects(ctx, projectID, bucket.ID); err != nil {
		return err
	}
	remaining, err := s.objects.ListObjects(ctx, projectID, bucket.ID, 1)
	if err != nil {
		return fmt.Errorf("verify bucket empty: %w", err)
	}
	if len(remaining) > 0 {
		return fmt.Errorf("bucket prefix still holds %d object(s) in the object store", len(remaining))
	}
	return s.store.DeleteBucket(ctx, projectID, name)
}

// purgeBucketObjects walks the catalogue page by page and deletes each
// object's bytes before its row, so a row never survives its bytes in the
// other direction. Stops at the first failure — the caller keeps the bucket.
func (s *Service) purgeBucketObjects(ctx context.Context, projectID, bucketID string) error {
	cursor := ""
	for {
		objs, next, err := s.store.ListObjects(ctx, bucketID, "", deleteBucketPageSize, cursor)
		if err != nil {
			return fmt.Errorf("list objects for cascade: %w", err)
		}
		for _, o := range objs {
			if err := s.purgeObject(ctx, projectID, bucketID, o.Key); err != nil {
				return err
			}
		}
		if next == "" {
			return nil
		}
		cursor = next
	}
}

// purgeUncataloguedObjects deletes the bytes left under a bucket's prefix
// that no catalogue row names. They were never charged to the quota, so
// nothing is released for them.
func (s *Service) purgeUncataloguedObjects(ctx context.Context, projectID, bucketID string) error {
	for {
		stored, err := s.objects.ListObjects(ctx, projectID, bucketID, deleteBucketPageSize)
		if err != nil {
			return fmt.Errorf("list stored objects: %w", err)
		}
		if len(stored) == 0 {
			return nil
		}
		for _, obj := range stored {
			if err := s.objects.DeleteObject(ctx, projectID, bucketID, obj.Key); err != nil {
				return fmt.Errorf("delete stray object bytes: %w", err)
			}
		}
		if len(stored) < deleteBucketPageSize {
			return nil
		}
	}
}

// purgeObject removes one object's bytes, then its row and quota charge
// together. Both steps report their own failure: a caller that saw success
// can rely on the bytes being gone and the project no longer paying for them.
// Re-running after a failure converges, because the row — and with it the
// size to release — survives until the release itself commits.
func (s *Service) purgeObject(ctx context.Context, projectID, bucketID, key string) error {
	if err := s.objects.DeleteObject(ctx, projectID, bucketID, key); err != nil {
		return fmt.Errorf("delete object bytes: %w", err)
	}
	if _, err := s.store.DeleteObjectAndReleaseQuota(ctx, projectID, bucketID, key); err != nil {
		return fmt.Errorf("delete object row: %w", err)
	}
	return nil
}

// SignUploadURL mints a presigned PUT URL after enforcing per-bucket and
// per-project quotas. The actual byte transfer goes client→R2 directly,
// bypassing Excalibase. The client MUST call ConfirmUpload after the PUT
// to record the object metadata.
func (s *Service) SignUploadURL(ctx context.Context, projectID, bucketName, tier string, req UploadURLRequest) (*UploadURLResponse, error) {
	bucket, err := s.uploadTarget(ctx, projectID, bucketName)
	if err != nil {
		return nil, err
	}
	if err := s.validateUploadRequest(ctx, projectID, tier, bucket, req); err != nil {
		return nil, err
	}

	mediaType, err := normaliseMIME(req.MimeType)
	if err != nil {
		return nil, err
	}
	url, expires, err := s.objects.SignedPutURL(ctx, projectID, bucket.ID, req.Key, mediaType, req.Size, uploadURLTTL)
	if err != nil {
		return nil, err
	}
	// The signature covers both headers, so the client must send exactly
	// these values or the PUT is rejected by the object store itself.
	headers := map[string]string{
		"Content-Type":   mediaType,
		"Content-Length": strconv.FormatInt(req.Size, 10),
	}
	return &UploadURLResponse{
		URL:       url,
		Method:    "PUT",
		Headers:   headers,
		ExpiresAt: expires,
	}, nil
}

// uploadTarget resolves the bucket an upload is destined for and refuses one
// whose bytes are being purged — accepting an upload there would race the
// emptiness check and leave the new object stranded under a deleted bucket.
func (s *Service) uploadTarget(ctx context.Context, projectID, bucketName string) (*Bucket, error) {
	if s.objects == nil {
		return nil, errObjectStoreUnset
	}
	bucket, err := s.store.GetBucket(ctx, projectID, bucketName)
	if err != nil {
		return nil, fmt.Errorf("load bucket: %w", err)
	}
	if bucket == nil {
		return nil, ErrBucketNotFound
	}
	if bucket.Status == BucketStatusDeleting {
		return nil, ErrBucketDeleting
	}
	return bucket, nil
}

// validateUploadRequest enforces per-bucket size limits, MIME allowlists,
// and per-project storage quotas before a presigned PUT URL is issued.
// Size and content type are required, not hints: a limit that is only
// checked when the caller volunteers the metadata is not a limit.
func (s *Service) validateUploadRequest(ctx context.Context, projectID, tier string, bucket *Bucket, req UploadURLRequest) error {
	if req.Size <= 0 {
		return invalidf("size is required and must be greater than zero")
	}
	mediaType, err := normaliseMIME(req.MimeType)
	if err != nil {
		return err
	}
	return s.enforceObjectLimits(ctx, projectID, tier, bucket, req.Size, mediaType)
}

// enforceObjectLimits applies the per-bucket size cap, the MIME allow-list
// and the per-project quota to one object. It runs twice per upload: against
// the declared metadata before a URL is issued, and against what the object
// store actually holds when the upload is confirmed.
func (s *Service) enforceObjectLimits(ctx context.Context, projectID, tier string, bucket *Bucket, size int64, mediaType string) error {
	if size <= 0 {
		return invalidf("object size must be greater than zero")
	}
	if bucket.FileSize > 0 && size > bucket.FileSize {
		return invalidf("file exceeds bucket size limit %d bytes", bucket.FileSize)
	}
	if err := checkMIMEAllowlist(bucket.AllowedTypes, mediaType); err != nil {
		return err
	}
	if err := checkPublicBucketType(bucket, mediaType); err != nil {
		return err
	}
	cap := s.quotaForTier(tier)
	if cap <= 0 {
		return nil
	}
	used, err := s.store.GetQuotaBytes(ctx, projectID)
	if err != nil {
		return fmt.Errorf("read quota: %w", err)
	}
	if used+size > cap {
		return invalidf("project storage quota exceeded (%d / %d bytes)", used, cap)
	}
	return nil
}

// ConfirmUpload records that a previously-signed upload completed. What the
// caller says it uploaded is not evidence: the size and content type are read
// back from the object store and the bucket's limits are applied to those.
// An object that breaks them is deleted and the confirmation refused, so a
// PUT that dodged the signed URL's constraints cannot become a stored object.
// Quota is charged the verified size.
func (s *Service) ConfirmUpload(ctx context.Context, projectID, bucketName, tier, ownerID string, req ConfirmUploadRequest) (*Object, error) {
	bucket, err := s.uploadTarget(ctx, projectID, bucketName)
	if err != nil {
		return nil, err
	}
	if req.Size < 0 {
		return nil, invalidf("size must not be negative")
	}
	stat, err := s.objects.HeadObject(ctx, projectID, bucket.ID, req.Key)
	if err != nil {
		if errors.Is(err, ErrObjectNotFound) {
			return nil, invalidf("no object stored at key %q", req.Key)
		}
		return nil, fmt.Errorf("inspect uploaded object: %w", err)
	}
	// The reaper deletes anything past the grace with no row. Refusing a
	// confirm before that point keeps the two windows apart, so a confirm can
	// never record a row for bytes the reaper is about to remove.
	if !stat.LastModified.IsZero() &&
		s.now().Sub(stat.LastModified) >= confirmWindow(DefaultUnconfirmedGrace) {
		return nil, invalidf("upload expired, request a new URL")
	}
	size, contentType, etag := stat.Size, stat.ContentType, stat.ETag
	mediaType, err := normaliseMIME(contentType)
	if err != nil {
		// The store holds something with no usable type; it cannot be
		// checked against the bucket's allow-list, so it does not stay.
		return nil, s.rejectStoredObject(ctx, projectID, bucket.ID, req.Key, err)
	}
	if err := s.enforceObjectLimits(ctx, projectID, tier, bucket, size, mediaType); err != nil {
		return nil, s.rejectStoredObject(ctx, projectID, bucket.ID, req.Key, err)
	}

	id, err := randomID("obj")
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	obj := &Object{
		ID:       id,
		BucketID: bucket.ID,
		Key:      req.Key,
		Size:     size,
		MimeType: mediaType,
		// The caller may carry its own digest (the runtime records a sha256
		// here); absent that, the store's ETag is what we have.
		ETag:      firstNonEmpty(req.ETag, etag),
		OwnerID:   ownerID,
		CreatedAt: now,
		UpdatedAt: now,
	}
	// The row and the charge are one write: a repeat of the same confirm is
	// a no-op on the usage, an overwrite moves it by the difference, and the
	// cap is decided by the write that spends against it.
	recorded, err := s.store.RecordObjectWithinQuota(ctx, projectID, obj, s.quotaForTier(tier))
	if err != nil {
		return nil, fmt.Errorf("record object: %w", err)
	}
	if !recorded {
		return nil, s.rejectStoredObject(ctx, projectID, bucket.ID, req.Key,
			invalidf("project storage quota exceeded"))
	}
	return obj, nil
}

// rejectStoredObject removes an object that must not be kept and returns the
// reason it was rejected. A failed cleanup is reported alongside it rather
// than hidden — the caller still sees the violation, and the reaper will
// collect what is left.
func (s *Service) rejectStoredObject(ctx context.Context, projectID, bucketID, key string, reason error) error {
	if err := s.objects.DeleteObject(ctx, projectID, bucketID, key); err != nil {
		return errors.Join(reason, fmt.Errorf("delete rejected object: %w", err))
	}
	return reason
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

// SignDownloadURL returns either a signed GET URL (private buckets) or
// the static public URL (public buckets). Public URLs don't expire.
func (s *Service) SignDownloadURL(ctx context.Context, projectID, bucketName, key string) (*DownloadURLResponse, error) {
	if s.objects == nil {
		return nil, errObjectStoreUnset
	}
	bucket, err := s.store.GetBucket(ctx, projectID, bucketName)
	if err != nil {
		return nil, fmt.Errorf("load bucket: %w", err)
	}
	if bucket == nil {
		return nil, ErrBucketNotFound
	}
	if bucket.Public {
		url, err := s.objects.PublicURL(projectID, bucket.ID, key)
		if err != nil {
			return nil, invalidf("invalid object key")
		}
		return &DownloadURLResponse{URL: url, Public: true}, nil
	}
	// A private object of a type the browser would execute is handed over as
	// a download: the catalogue knows what was stored, so the signature can
	// say how it must be served.
	download, err := s.servesAsDownload(ctx, bucket.ID, key)
	if err != nil {
		return nil, err
	}
	url, expires, err := s.objects.SignedGetURL(ctx, projectID, bucket.ID, key, download, 5*time.Minute)
	if err != nil {
		return nil, err
	}
	return &DownloadURLResponse{URL: url, ExpiresAt: expires, Public: false}, nil
}

// servesAsDownload reports whether an object's recorded type is one a browser
// would render. An object with no row yet is served as-is: there is nothing
// to go on, and the signed URL only reaches someone already authorised.
func (s *Service) servesAsDownload(ctx context.Context, bucketID, key string) (bool, error) {
	obj, err := s.store.GetObject(ctx, bucketID, key)
	if err != nil {
		return false, fmt.Errorf("load object: %w", err)
	}
	if obj == nil {
		return false, nil
	}
	mediaType, err := normaliseMIME(obj.MimeType)
	if err != nil {
		// An unusable recorded type is exactly the case not to render.
		return true, nil
	}
	return renderableTypes[mediaType], nil
}

// ListObjects pages through metadata. Doesn't hit R2 — the catalogue is
// authoritative.
func (s *Service) ListObjects(ctx context.Context, projectID, bucketName string, req ListObjectsRequest) (*ListObjectsResponse, error) {
	bucket, err := s.store.GetBucket(ctx, projectID, bucketName)
	if err != nil {
		return nil, fmt.Errorf("load bucket: %w", err)
	}
	if bucket == nil {
		return nil, ErrBucketNotFound
	}
	if req.Limit <= 0 || req.Limit > 1000 {
		req.Limit = 100
	}
	objs, next, err := s.store.ListObjects(ctx, bucket.ID, req.Prefix, req.Limit, req.Cursor)
	if err != nil {
		return nil, fmt.Errorf("list objects: %w", err)
	}
	return &ListObjectsResponse{Objects: objs, NextCursor: next}, nil
}

// DeleteObject removes an object from both the blob plane and the catalogue.
// Bytes first, then the row: a caller that retries after a failure can always
// find the object again through its row, whereas bytes without a row are
// unreachable. Both must succeed for the call to report success. A bucket
// that does not exist yields ErrBucketNotFound; an object that is already
// gone from the blob plane counts as deleted, so retries converge.
func (s *Service) DeleteObject(ctx context.Context, projectID, bucketName, key string) error {
	if s.objects == nil {
		return errObjectStoreUnset
	}
	bucket, err := s.store.GetBucket(ctx, projectID, bucketName)
	if err != nil {
		return fmt.Errorf("load bucket: %w", err)
	}
	if bucket == nil {
		return ErrBucketNotFound
	}
	return s.purgeObject(ctx, projectID, bucket.ID, key)
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
		return invalidf("bucket name must be 3-63 chars, got %d", len(name))
	}
	for i, ch := range name {
		switch {
		case ch >= 'a' && ch <= 'z', ch >= '0' && ch <= '9':
			// ok
		case ch == '-':
			if i == 0 || i == len(name)-1 {
				return invalidf("bucket name can't start/end with hyphen")
			}
		default:
			return invalidf("bucket name must be lowercase letters/digits/hyphens only")
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

