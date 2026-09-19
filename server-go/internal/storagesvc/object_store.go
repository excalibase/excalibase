package storagesvc

import (
	"context"
	"time"
)

// ObjectStore is the blob plane the Service delegates to: presigned URLs,
// deletes, and the listing used to prove a bucket's prefix really is empty
// before its metadata is dropped. *R2Client is the production implementation;
// depending on the interface instead of the concrete client lets tests drive
// failure modes (a store that rejects deletes) that a live endpoint cannot.
//
// Every method takes the bucket's ID, never its name: the key namespace is
// derived from it, and only an identity that is never reused keeps an
// outstanding presigned URL from addressing a later bucket's objects. Names
// are resolved to ids at the API boundary.
type ObjectStore interface {
	SignedPutURL(ctx context.Context, projectID, bucketID, key, mimeType string, size int64, ttl time.Duration) (string, time.Time, error)
	// SignedGetURL mints a read URL. When download is set, the signature also
	// pins the response's Content-Disposition and Content-Type, so the object
	// is handed to the browser as a file instead of being rendered.
	SignedGetURL(ctx context.Context, projectID, bucketID, key string, download bool, ttl time.Duration) (string, time.Time, error)
	PublicURL(projectID, bucketID, key string) (string, error)
	// DeleteObject is idempotent: an object that is already gone is success.
	DeleteObject(ctx context.Context, projectID, bucketID, key string) error
	// HeadObject reports what the store actually holds for a key. Returns
	// ErrObjectNotFound when there is nothing there.
	HeadObject(ctx context.Context, projectID, bucketID, key string) (ObjectStat, error)
	// ListObjects returns up to limit objects stored under the bucket's own
	// prefix, keyed relative to the bucket. Used to verify emptiness before a
	// bucket's metadata is dropped, so it must never see a neighbouring
	// bucket's keys.
	ListObjects(ctx context.Context, projectID, bucketID string, limit int32) ([]StoredObject, error)
	// CopyObject moves an object's bytes onto another key within the same
	// bucket, server-side. An upload becomes the object this way, so the
	// key's previous contents are replaced only once the new bytes have been
	// read back and accepted.
	CopyObject(ctx context.Context, projectID, bucketID, sourceKey, destinationKey string) error
	// ListStagedUploads returns up to limit uploads that are staged but not
	// yet accepted, youngest first is not required. It is the reaper's only
	// listing: an interface that cannot name a live key cannot collect one.
	ListStagedUploads(ctx context.Context, projectID, bucketID string, limit int32) ([]StagedUpload, error)
	// DeleteStagingObject removes one staged upload by its id. The key is
	// built from the id, so no caller can steer this at an object.
	DeleteStagingObject(ctx context.Context, projectID, bucketID, uploadID string) error
	// ListKeysWithPrefix returns up to limit whole store keys under an
	// arbitrary prefix. It is the one listing primitive: the bucket-scoped
	// views above are built on it. A project teardown needs it because by
	// then there is no catalogue left to enumerate the project's objects —
	// not its buckets, not its rows, not even the uploads it never
	// confirmed.
	ListKeysWithPrefix(ctx context.Context, prefix string, limit int32) ([]string, error)
	// DeleteKey removes one whole store key, refusing any key that does not
	// fall under prefix — the caller's own namespace — so a listing that
	// returned something unexpected can never be turned into a delete
	// somewhere else. It is the one delete primitive: DeleteObject and
	// DeleteStagingObject are guarded views of it.
	DeleteKey(ctx context.Context, prefix, key string) error
}

// ObjectStat is what the object store says about one stored object. It is
// the only account of an upload the platform trusts: the caller's claims
// about size and type are not evidence, and the write time decides whether a
// confirmation is still in time.
type ObjectStat struct {
	Size         int64
	ContentType  string
	ETag         string
	LastModified time.Time
}

// StagedUpload is one upload waiting to be accepted: the id it was issued
// under and when its bytes were written. The reaper collects the ones nobody
// came back for.
type StagedUpload struct {
	UploadID     string
	LastModified time.Time
}
