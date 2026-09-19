// Package storagesvc implements Supabase-style file storage on top of an
// S3-compatible backend (Cloudflare R2 in production). Two layers:
//
//   - R2Client: thin S3 SDK wrapper that knows about presigned URLs,
//     multipart uploads, and the per-project key prefix scheme.
//   - Storage service (separate file): bucket + object metadata persisted
//     in platform-db, business logic for quotas, access control, signed-
//     URL minting.
//
// The split lets us mock out R2 in tests while still exercising the
// metadata + RBAC paths against a real platform-db.
package storagesvc

import "time"

// Bucket is the logical grouping users see. Maps 1:1 to a key prefix in
// the underlying R2 bucket: projects/{projectId}/buckets/{name}/.
//
// Public=true means the prefix can be read without auth via the public
// path (/storage/v1/object/public/{bucket}/{key}). Public buckets still
// require auth to UPLOAD — public is read-only-from-the-internet.
// Bucket lifecycle states. A bucket is "active" from creation; it moves to
// "deleting" before any of its bytes are removed and never moves back — the
// row itself goes when the delete completes. The marker is what keeps a
// half-finished cascade from looking like a healthy bucket.
const (
	BucketStatusActive   = "active"
	BucketStatusDeleting = "deleting"
)

type Bucket struct {
	ID         string    `json:"id"`
	ProjectID  string    `json:"projectId"`
	Name       string    `json:"name"`
	Public     bool      `json:"public"`
	Status     string    `json:"status,omitempty"`
	FileSize   int64     `json:"fileSizeLimit,omitempty"`   // optional per-bucket cap (bytes)
	AllowedTypes []string `json:"allowedMimeTypes,omitempty"` // optional MIME allowlist
	CreatedAt  time.Time `json:"createdAt"`
	UpdatedAt  time.Time `json:"updatedAt"`
}

// Object is a single file. ETag is whatever the underlying store returned
// (R2 returns S3-style MD5-of-md5s for multipart, MD5 for single-part);
// useful for cache validation and resumable uploads.
type Object struct {
	ID        string    `json:"id"`
	BucketID  string    `json:"bucketId"`
	Key       string    `json:"key"`        // path within the bucket, e.g. avatars/123.png
	Size      int64     `json:"size"`
	MimeType  string    `json:"mimeType"`
	ETag      string    `json:"etag,omitempty"`
	OwnerID   string    `json:"ownerId,omitempty"` // user id, for audit; not enforced by RLS yet
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// CreateBucketRequest is the input shape for POST /buckets. Validation
// happens at the handler boundary; the service trusts these fields.
type CreateBucketRequest struct {
	Name             string   `json:"name"`
	Public           bool     `json:"public,omitempty"`
	FileSizeLimit    int64    `json:"fileSizeLimit,omitempty"`
	AllowedMimeTypes []string `json:"allowedMimeTypes,omitempty"`
}

// UploadURLRequest carries the metadata we need to mint a signed PUT URL.
// We don't accept the file bytes through Excalibase — clients PUT directly
// to R2 with the URL we return, saving a streaming round-trip.
type UploadURLRequest struct {
	Key      string `json:"key"`
	MimeType string `json:"mimeType,omitempty"` // pre-set Content-Type on the signed URL
	Size     int64  `json:"size,omitempty"`     // hint for quota check; not enforced on the URL itself
}

// UploadURLResponse is what the frontend uses to PUT the bytes directly
// to R2. After the PUT succeeds the client calls confirm-upload so we
// can record the object metadata; absent that confirmation the file is
// orphaned in R2 and a daily janitor reaps it.
type UploadURLResponse struct {
	URL       string            `json:"url"`
	Method    string            `json:"method"` // always "PUT" for now
	Headers   map[string]string `json:"headers"`
	ExpiresAt time.Time         `json:"expiresAt"`
}

// DownloadURLResponse is the signed GET URL or the public CDN URL,
// depending on bucket visibility.
type DownloadURLResponse struct {
	URL       string    `json:"url"`
	ExpiresAt time.Time `json:"expiresAt,omitempty"` // omitted for public buckets (URL never expires)
	Public    bool      `json:"public"`
}

// ListObjectsRequest pages through a bucket. Prefix narrows the scan to a
// "folder" within the bucket (key prefix; R2 has no folders).
type ListObjectsRequest struct {
	Prefix string `json:"prefix,omitempty"`
	Cursor string `json:"cursor,omitempty"` // opaque continuation token
	Limit  int    `json:"limit,omitempty"`  // default 100, max 1000
}

// ListObjectsResponse — plain page-of-objects with a cursor for the next
// page. Empty NextCursor means we're at the end.
type ListObjectsResponse struct {
	Objects    []Object `json:"objects"`
	NextCursor string   `json:"nextCursor,omitempty"`
}

// ConfirmUploadRequest tells the service "the PUT to your signed URL
// finished, please record this object." Size + ETag are what the client
// observed (the signed URL's response headers carry them); the service
// trusts the values for now and we'll add a HEAD-back-to-R2 check in
// v1.2 once we see traffic shape.
type ConfirmUploadRequest struct {
	Key      string `json:"key"`
	Size     int64  `json:"size"`
	MimeType string `json:"mimeType"`
	ETag     string `json:"etag,omitempty"`
}
