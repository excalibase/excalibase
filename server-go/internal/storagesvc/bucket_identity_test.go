package storagesvc

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

// EXC-404 follow-up, finding 1 — a presigned PUT stays valid for its whole
// TTL and cannot be revoked. If the object-store prefix is derived from the
// bucket NAME, deleting a bucket and re-creating it under the same name hands
// that outstanding URL a write into the new bucket's space: bytes nobody
// catalogued or billed, inside someone's live bucket, and the emptiness check
// that guarded the delete has already passed.
//
// The prefix therefore has to come from the bucket's immutable id.

// newEmptyStoreService wires a service whose blob plane reports an empty
// bucket and accepts deletes, so bucket lifecycle can be driven end to end.
func newEmptyStoreService(t *testing.T, store BucketStore) *Service {
	t.Helper()
	r2 := newStubbedR2(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			_, _ = w.Write([]byte(`<ListBucketResult xmlns="http://s3.amazonaws.com/doc/2006-03-01/"></ListBucketResult>`))
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	return NewService(store, r2, nil)
}

func signedKeyPath(t *testing.T, signedURL string) string {
	t.Helper()
	parsed, err := url.Parse(signedURL)
	if err != nil {
		t.Fatalf("parse signed url: %v", err)
	}
	return parsed.Path
}

func TestService_ObjectKeysAreNamespacedByBucketIdentityNotName(t *testing.T) {
	store := newMemStore()
	svc := newEmptyStoreService(t, store)
	ctx := context.Background()

	first, err := svc.CreateBucket(ctx, testProjX, CreateBucketRequest{Name: "assets"})
	if err != nil {
		t.Fatalf("CreateBucket: %v", err)
	}
	outstanding, err := svc.SignUploadURL(ctx, testProjX, "assets", "FREE", UploadURLRequest{
		Key: "payload", MimeType: "image/png", Size: 10,
	})
	if err != nil {
		t.Fatalf("SignUploadURL: %v", err)
	}

	if err := svc.DeleteBucket(ctx, testProjX, "assets"); err != nil {
		t.Fatalf("DeleteBucket: %v", err)
	}
	second, err := svc.CreateBucket(ctx, testProjX, CreateBucketRequest{Name: "assets"})
	if err != nil {
		t.Fatalf("re-create bucket: %v", err)
	}
	if first.ID == second.ID {
		t.Fatal("a re-created bucket must be a new identity")
	}
	reissued, err := svc.SignUploadURL(ctx, testProjX, "assets", "FREE", UploadURLRequest{
		Key: "payload", MimeType: "image/png", Size: 10,
	})
	if err != nil {
		t.Fatalf("SignUploadURL after re-create: %v", err)
	}

	oldPath := signedKeyPath(t, outstanding.URL)
	newPath := signedKeyPath(t, reissued.URL)
	if oldPath == newPath {
		t.Fatalf("the URL issued for the deleted bucket still addresses the new bucket's key: %s", oldPath)
	}
	if !strings.Contains(newPath, second.ID) {
		t.Errorf("object key should carry the bucket id, got %s", newPath)
	}
	if strings.Contains(newPath, first.ID) {
		t.Errorf("object key must not carry the previous bucket's id, got %s", newPath)
	}
}

// The same identity has to run through every path that touches the blob
// plane, or one of them re-opens the hole.
func TestService_AllBlobPathsUseBucketIdentity(t *testing.T) {
	store := newMemStore()
	svc := newEmptyStoreService(t, store)
	ctx := context.Background()
	bucket, err := svc.CreateBucket(ctx, testProjX, CreateBucketRequest{Name: "assets", Public: true})
	if err != nil {
		t.Fatalf("CreateBucket: %v", err)
	}

	download, err := svc.SignDownloadURL(ctx, testProjX, "assets", "a.png")
	if err != nil {
		t.Fatalf("SignDownloadURL: %v", err)
	}
	if !strings.Contains(download.URL, bucket.ID) {
		t.Errorf("public URL should carry the bucket id: %s", download.URL)
	}
	if strings.Contains(download.URL, "/buckets/assets/") {
		t.Errorf("public URL must not be namespaced by the bucket name: %s", download.URL)
	}

	storeKey, _, err := svc.StartResumableUpload(ctx, testProjX, "assets", "FREE", UploadURLRequest{
		Key: "clip.mp4", MimeType: "video/mp4", Size: 10,
	})
	if err != nil {
		t.Fatalf("StartResumableUpload: %v", err)
	}
	if !strings.Contains(storeKey, bucket.ID) {
		t.Errorf("resumable upload key should carry the bucket id: %s", storeKey)
	}
}
