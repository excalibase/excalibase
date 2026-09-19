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
	SignedPutURL(ctx context.Context, projectID, bucketID, key, mimeType string, ttl time.Duration) (string, time.Time, error)
	SignedGetURL(ctx context.Context, projectID, bucketID, key string, ttl time.Duration) (string, time.Time, error)
	PublicURL(projectID, bucketID, key string) (string, error)
	// DeleteObject is idempotent: an object that is already gone is success.
	DeleteObject(ctx context.Context, projectID, bucketID, key string) error
	// ListObjectKeys returns up to limit keys stored under the bucket's own
	// prefix. Used to verify emptiness, so it must never see a neighbouring
	// bucket's keys.
	ListObjectKeys(ctx context.Context, projectID, bucketID string, limit int32) ([]string, error)
}
