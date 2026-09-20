package storagesvc

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// errInjectingStore wraps memStore and fails one chosen operation, so the
// service's "report what happened" contract can be checked per step.
type errInjectingStore struct {
	*memStore
	deleteObjectErr error
	quotaErr        error
	releaseQuotaErr error
	getBucketErr    error
	createObjectErr error
	quotaReadErr    error
	listBucketErr   error
	recordObjectErr error
	getObjectErr    error
	listObjectsErr  error
	setStatusErr    error
	statusWrites    []string
}

func (e *errInjectingStore) GetBucket(ctx context.Context, projectID, name string) (*Bucket, error) {
	if e.getBucketErr != nil {
		return nil, e.getBucketErr
	}
	return e.memStore.GetBucket(ctx, projectID, name)
}

func (e *errInjectingStore) SetBucketStatus(ctx context.Context, projectID, name, status string) error {
	if e.setStatusErr != nil {
		return e.setStatusErr
	}
	e.statusWrites = append(e.statusWrites, status)
	return e.memStore.SetBucketStatus(ctx, projectID, name, status)
}

func (e *errInjectingStore) GetObject(ctx context.Context, bucketID, key string) (*Object, error) {
	if e.getObjectErr != nil {
		return nil, e.getObjectErr
	}
	return e.memStore.GetObject(ctx, bucketID, key)
}

func (e *errInjectingStore) RecordObjectWithinQuota(ctx context.Context, projectID string, o *Object, capBytes int64) (bool, error) {
	if e.recordObjectErr != nil {
		return false, e.recordObjectErr
	}
	return e.memStore.RecordObjectWithinQuota(ctx, projectID, o, capBytes)
}

func (e *errInjectingStore) CreateObject(ctx context.Context, o *Object) error {
	if e.createObjectErr != nil {
		return e.createObjectErr
	}
	return e.memStore.CreateObject(ctx, o)
}

func (e *errInjectingStore) ListObjects(ctx context.Context, bucketID, prefix string, limit int, cursor string) ([]Object, string, error) {
	if e.listObjectsErr != nil {
		return nil, "", e.listObjectsErr
	}
	return e.memStore.ListObjects(ctx, bucketID, prefix, limit, cursor)
}

func (e *errInjectingStore) DeleteObjectAndReleaseQuota(ctx context.Context, projectID, bucketID, key string) (bool, error) {
	if e.deleteObjectErr != nil {
		return false, e.deleteObjectErr
	}
	// The row and its release commit together, so an injected failure here
	// leaves both in place — exactly what the SQL transaction guarantees.
	if e.releaseQuotaErr != nil {
		return false, e.releaseQuotaErr
	}
	return e.memStore.DeleteObjectAndReleaseQuota(ctx, projectID, bucketID, key)
}

func (e *errInjectingStore) GetQuotaBytes(ctx context.Context, projectID string) (int64, error) {
	if e.quotaReadErr != nil {
		return 0, e.quotaReadErr
	}
	return e.memStore.GetQuotaBytes(ctx, projectID)
}

func (e *errInjectingStore) ListAllBuckets(ctx context.Context) ([]Bucket, error) {
	if e.listBucketErr != nil {
		return nil, e.listBucketErr
	}
	return e.memStore.ListAllBuckets(ctx)
}

func (e *errInjectingStore) AddQuotaBytes(ctx context.Context, projectID string, delta int64) error {
	if e.quotaErr != nil {
		return e.quotaErr
	}
	return e.memStore.AddQuotaBytes(ctx, projectID, delta)
}

func newErrStore() *errInjectingStore { return &errInjectingStore{memStore: newMemStore()} }

// seedBucket creates a bucket plus one object present in both planes. The
// blob plane is addressed by the bucket's id, the way the service does.
func seedBucket(t *testing.T, svc *Service, blobs *fakeObjectStore, name, key string, size int64) *Bucket {
	t.Helper()
	ctx := context.Background()
	bucket, err := svc.CreateBucket(ctx, testProjX, CreateBucketRequest{Name: name})
	if err != nil {
		t.Fatalf("CreateBucket: %v", err)
	}
	blobs.put(testProjX, bucket.ID, stagingObjectKey(testUploadID(key)), size, "text/plain")
	if _, err := svc.ConfirmUpload(ctx, testProjX, name, "FREE", "u", ConfirmUploadRequest{Key: key, UploadID: testUploadID(key)}); err != nil {
		t.Fatalf("ConfirmUpload: %v", err)
	}
	return bucket
}

// The bucket is marked deleting BEFORE any byte is removed, so a crash
// mid-cascade leaves an explicit state rather than a bucket that looks fine
// while its rows point at nothing.
func TestService_DeleteBucket_MarksDeletingBeforeRemovingBytes(t *testing.T) {
	store := newErrStore()
	blobs := newFakeObjectStore()
	svc := NewServiceWithObjectStore(store, blobs, nil)
	seedBucket(t, svc, blobs, "assets", "a.txt", 10)
	blobs.deleteErr = errors.New("object store refuses deletes")

	if err := svc.DeleteBucket(context.Background(), testProjX, "assets"); err == nil {
		t.Fatal("expected the delete to fail")
	}
	if len(store.statusWrites) != 1 || store.statusWrites[0] != BucketStatusDeleting {
		t.Fatalf("bucket should have been marked deleting first, got %v", store.statusWrites)
	}
	b, _ := store.GetBucket(context.Background(), testProjX, "assets")
	if b == nil || b.Status != BucketStatusDeleting {
		t.Fatalf("bucket should remain, marked deleting: %+v", b)
	}
}

// A retry of a half-finished delete completes it, and does not re-mark a
// bucket that is already deleting.
func TestService_DeleteBucket_RetryCompletesAfterFailure(t *testing.T) {
	store := newErrStore()
	blobs := newFakeObjectStore()
	svc := NewServiceWithObjectStore(store, blobs, nil)
	seedBucket(t, svc, blobs, "assets", "a.txt", 10)
	blobs.deleteErr = errors.New("object store refuses deletes")
	ctx := context.Background()
	_ = svc.DeleteBucket(ctx, testProjX, "assets")

	blobs.deleteErr = nil
	if err := svc.DeleteBucket(ctx, testProjX, "assets"); err != nil {
		t.Fatalf("retry should complete the delete: %v", err)
	}
	if b, _ := store.GetBucket(ctx, testProjX, "assets"); b != nil {
		t.Error("bucket record should be gone after a successful retry")
	}
	if len(store.statusWrites) != 1 {
		t.Errorf("bucket already deleting should not be re-marked: %v", store.statusWrites)
	}
}

// The emptiness check must key off the bucket's own prefix. "assets2" is not
// part of "assets", in either direction.
func TestService_DeleteBucket_PrefixDoesNotMatchNeighbourBucket(t *testing.T) {
	store := newMemStore()
	blobs := newFakeObjectStore()
	svc := NewServiceWithObjectStore(store, blobs, nil)
	ctx := context.Background()
	_, _ = svc.CreateBucket(ctx, testProjX, CreateBucketRequest{Name: "assets"})
	neighbour, _ := svc.CreateBucket(ctx, testProjX, CreateBucketRequest{Name: "assets2"})
	blobs.put(testProjX, neighbour.ID, "keep.bin", 10, "text/plain")

	if err := svc.DeleteBucket(ctx, testProjX, "assets"); err != nil {
		t.Fatalf("a neighbour bucket's objects must not block this delete: %v", err)
	}
	if !blobs.has(testProjX, neighbour.ID, "keep.bin") {
		t.Error("neighbour bucket's object was deleted")
	}
}

// A bucket being emptied accepts no new uploads: one arriving after the
// emptiness check would be stranded under a bucket that no longer exists.
func TestService_Uploads_RejectedWhileBucketIsDeleting(t *testing.T) {
	store := newMemStore()
	blobs := newFakeObjectStore()
	svc := NewServiceWithObjectStore(store, blobs, nil)
	ctx := context.Background()
	_, _ = svc.CreateBucket(ctx, testProjX, CreateBucketRequest{Name: "assets"})
	_ = store.SetBucketStatus(ctx, testProjX, "assets", BucketStatusDeleting)

	if _, err := svc.SignUploadURL(ctx, testProjX, "assets", "FREE", UploadURLRequest{Key: "x", Size: 1}); !errors.Is(err, ErrBucketDeleting) {
		t.Errorf("SignUploadURL: want ErrBucketDeleting, got %v", err)
	}
	if _, err := svc.ConfirmUpload(ctx, testProjX, "assets", "FREE", "u", ConfirmUploadRequest{Key: "x", UploadID: testUploadID("x")}); !errors.Is(err, ErrBucketDeleting) {
		t.Errorf("ConfirmUpload: want ErrBucketDeleting, got %v", err)
	}
	if _, _, err := svc.StartResumableUpload(ctx, testProjX, "assets", "FREE", UploadURLRequest{Key: "x", Size: 1}); !errors.Is(err, ErrBucketDeleting) {
		t.Errorf("StartResumableUpload: want ErrBucketDeleting, got %v", err)
	}
}

func TestService_DeleteBucket_ReportsStatusWriteFailure(t *testing.T) {
	store := newErrStore()
	svc := NewServiceWithObjectStore(store, newFakeObjectStore(), nil)
	_, _ = svc.CreateBucket(context.Background(), testProjX, CreateBucketRequest{Name: "assets"})
	store.setStatusErr = errors.New("platform db unavailable")

	if err := svc.DeleteBucket(context.Background(), testProjX, "assets"); err == nil {
		t.Fatal("a failed status write must not be ignored")
	}
}

func TestService_DeleteBucket_ReportsCatalogueFailure(t *testing.T) {
	store := newErrStore()
	blobs := newFakeObjectStore()
	svc := NewServiceWithObjectStore(store, blobs, nil)
	seedBucket(t, svc, blobs, "assets", "a.txt", 10)
	store.deleteObjectErr = errors.New("platform db unavailable")

	err := svc.DeleteBucket(context.Background(), testProjX, "assets")
	if err == nil || !strings.Contains(err.Error(), "delete object row") {
		t.Fatalf("catalogue failure must surface, got %v", err)
	}
}

// The release commits with the row, so a failure there fails the delete and
// leaves both in place for the retry.
func TestService_DeleteBucket_ReportsQuotaReleaseFailure(t *testing.T) {
	store := newErrStore()
	blobs := newFakeObjectStore()
	svc := NewServiceWithObjectStore(store, blobs, nil)
	seedBucket(t, svc, blobs, "assets", "a.txt", 10)
	store.releaseQuotaErr = errors.New("platform db unavailable")

	err := svc.DeleteBucket(context.Background(), testProjX, "assets")
	if err == nil || !strings.Contains(err.Error(), "delete object row") {
		t.Fatalf("release failure must surface, got %v", err)
	}
	if used, _ := store.GetQuotaBytes(context.Background(), testProjX); used != 10 {
		t.Errorf("a failed release must not half-apply: got %d, want the original 10", used)
	}
}

func TestService_DeleteObject_ReportsCatalogueFailure(t *testing.T) {
	store := newErrStore()
	blobs := newFakeObjectStore()
	svc := NewServiceWithObjectStore(store, blobs, nil)
	seedBucket(t, svc, blobs, "assets", "a.txt", 10)
	store.deleteObjectErr = errors.New("platform db unavailable")

	err := svc.DeleteObject(context.Background(), testProjX, "assets", "a.txt")
	if err == nil || !strings.Contains(err.Error(), "delete object row") {
		t.Fatalf("catalogue failure must surface, got %v", err)
	}
}

// An object the blob plane no longer has is a completed delete, not a
// failure — that is what makes a retried delete converge.
func TestService_DeleteObject_AlreadyGoneIsSuccess(t *testing.T) {
	store := newMemStore()
	blobs := newFakeObjectStore()
	svc := NewServiceWithObjectStore(store, blobs, nil)
	ctx := context.Background()
	_, _ = svc.CreateBucket(ctx, testProjX, CreateBucketRequest{Name: "assets"})

	if err := svc.DeleteObject(ctx, testProjX, "assets", "never-existed"); err != nil {
		t.Fatalf("deleting an absent object should succeed: %v", err)
	}
}

func TestService_ConfirmUpload_ReportsWriteFailure(t *testing.T) {
	store := newErrStore()
	blobs := newFakeObjectStore()
	svc := NewServiceWithObjectStore(store, blobs, nil)
	ctx := context.Background()
	_, _ = svc.CreateBucket(ctx, testProjX, CreateBucketRequest{Name: "assets"})
	store.recordObjectErr = errors.New("platform db unavailable")

	blobs.put(testProjX, bucketOf(t, store, "assets"), stagingObjectKey(testUploadID("a.txt")), 10, "text/plain")
	_, err := svc.ConfirmUpload(ctx, testProjX, "assets", "FREE", "u", ConfirmUploadRequest{Key: "a.txt", UploadID: testUploadID("a.txt")})
	if err == nil || !strings.Contains(err.Error(), "record object") {
		t.Fatalf("an unrecorded upload must not report success, got %v", err)
	}
}

func TestService_CreateBucket_ReportsLookupFailure(t *testing.T) {
	store := newErrStore()
	svc := NewServiceWithObjectStore(store, newFakeObjectStore(), nil)
	store.getBucketErr = errors.New("platform db unavailable")

	_, err := svc.CreateBucket(context.Background(), testProjX, CreateBucketRequest{Name: "assets"})
	if err == nil || !strings.Contains(err.Error(), "check existing bucket") {
		t.Fatalf("a failed uniqueness check must not be read as 'no duplicate', got %v", err)
	}
}

// Every path that needs the blob plane must say so rather than panic when it
// is not configured.
func TestService_ReportsUnconfiguredObjectStore(t *testing.T) {
	store := newMemStore()
	svc := NewService(store, nil, nil)
	ctx := context.Background()
	_, _ = svc.CreateBucket(ctx, testProjX, CreateBucketRequest{Name: "assets"})

	if err := svc.DeleteBucket(ctx, testProjX, "assets"); err == nil {
		t.Error("DeleteBucket should report the missing object store")
	}
	if err := svc.DeleteObject(ctx, testProjX, "assets", "k"); err == nil {
		t.Error("DeleteObject should report the missing object store")
	}
	if _, err := svc.SignDownloadURL(ctx, testProjX, "assets", "k"); err == nil {
		t.Error("SignDownloadURL should report the missing object store")
	}
	if _, err := svc.SignUploadURL(ctx, testProjX, "assets", "FREE", UploadURLRequest{Key: "k"}); err == nil {
		t.Error("SignUploadURL should report the missing object store")
	}
}

func TestService_SignDownloadURL_PublicBucketRejectsInvalidKey(t *testing.T) {
	store := newMemStore()
	svc := NewServiceWithObjectStore(store, newFakeObjectStore(), nil)
	ctx := context.Background()
	_, _ = svc.CreateBucket(ctx, testProjX, CreateBucketRequest{Name: "pub", Public: true})

	if _, err := svc.SignDownloadURL(ctx, testProjX, "pub", ""); err == nil {
		t.Error("an unusable key must not yield an empty URL and a success")
	}
}

// Every entry point that loads a bucket must report a lookup failure instead
// of reading it as "no such bucket" — the two have opposite consequences.
func TestService_ReportsBucketLookupFailure(t *testing.T) {
	store := newErrStore()
	blobs := newFakeObjectStore()
	svc := NewServiceWithObjectStore(store, blobs, nil)
	ctx := context.Background()
	_, _ = svc.CreateBucket(ctx, testProjX, CreateBucketRequest{Name: "assets"})
	store.getBucketErr = errors.New("platform db unavailable")

	calls := map[string]func() error{
		"DeleteBucket":    func() error { return svc.DeleteBucket(ctx, testProjX, "assets") },
		"DeleteObject":    func() error { return svc.DeleteObject(ctx, testProjX, "assets", "k") },
		"SignUploadURL":   func() error { _, e := svc.SignUploadURL(ctx, testProjX, "assets", "FREE", UploadURLRequest{Key: "k"}); return e },
		"ConfirmUpload":   func() error { _, e := svc.ConfirmUpload(ctx, testProjX, "assets", "FREE", "u", ConfirmUploadRequest{Key: "k", UploadID: testUploadID("k")}); return e },
		"SignDownloadURL": func() error { _, e := svc.SignDownloadURL(ctx, testProjX, "assets", "k"); return e },
		"ListObjects":     func() error { _, e := svc.ListObjects(ctx, testProjX, "assets", ListObjectsRequest{}); return e },
	}
	for name, call := range calls {
		err := call()
		if err == nil || errors.Is(err, ErrBucketNotFound) {
			t.Errorf("%s: a lookup failure must not read as bucket-not-found, got %v", name, err)
		}
	}
}

func TestService_ConfirmUpload_ReportsRecordFailure(t *testing.T) {
	store := newErrStore()
	blobs := newFakeObjectStore()
	svc := NewServiceWithObjectStore(store, blobs, nil)
	ctx := context.Background()
	_, _ = svc.CreateBucket(ctx, testProjX, CreateBucketRequest{Name: "assets"})
	store.recordObjectErr = errors.New("platform db unavailable")

	blobs.put(testProjX, bucketOf(t, store, "assets"), stagingObjectKey(testUploadID("k")), 1, "text/plain")
	_, err := svc.ConfirmUpload(ctx, testProjX, "assets", "FREE", "u", ConfirmUploadRequest{Key: "k", UploadID: testUploadID("k")})
	if err == nil || !strings.Contains(err.Error(), "record object") {
		t.Fatalf("want a record-object failure, got %v", err)
	}
}

func TestService_ListObjects_ReportsStoreFailure(t *testing.T) {
	store := newErrStore()
	svc := NewServiceWithObjectStore(store, newFakeObjectStore(), nil)
	ctx := context.Background()
	_, _ = svc.CreateBucket(ctx, testProjX, CreateBucketRequest{Name: "assets"})
	store.listObjectsErr = errors.New("platform db unavailable")

	if _, err := svc.ListObjects(ctx, testProjX, "assets", ListObjectsRequest{}); err == nil {
		t.Fatal("a failed listing must not look like an empty bucket")
	}
	if err := svc.DeleteBucket(ctx, testProjX, "assets"); err == nil {
		t.Fatal("a failed cascade listing must not let the bucket record go")
	}
}

// A presign that fails is not a URL: SignUploadURL must not answer with one.
func TestService_SignUploadURL_ReportsPresignFailure(t *testing.T) {
	store := newMemStore()
	blobs := newFakeObjectStore()
	blobs.signErr = errors.New("presign unavailable")
	svc := NewServiceWithObjectStore(store, blobs, nil)
	ctx := context.Background()
	_, _ = svc.CreateBucket(ctx, testProjX, CreateBucketRequest{Name: "assets"})

	if _, err := svc.SignUploadURL(ctx, testProjX, "assets", "FREE", UploadURLRequest{Key: "k", Size: 1}); err == nil {
		t.Fatal("a failed presign must surface")
	}
	if _, err := svc.SignDownloadURL(ctx, testProjX, "assets", "k"); err == nil {
		t.Fatal("a failed presign must surface on download too")
	}
}

// EXC-404 follow-up, finding 2 — releasing the quota is not a separate step
// that may be lost. If the row goes and the release does not, the retry finds
// no row, computes a size of zero and the bytes are charged to the project
// forever. Row and release therefore move together.
func TestService_DeleteObject_RetryReleasesQuotaExactlyOnce(t *testing.T) {
	store := newErrStore()
	blobs := newFakeObjectStore()
	svc := NewServiceWithObjectStore(store, blobs, nil)
	ctx := context.Background()
	seedBucket(t, svc, blobs, "assets", "a.txt", 300)
	if used, _ := store.GetQuotaBytes(ctx, testProjX); used != 300 {
		t.Fatalf("pre-delete quota: got %d, want 300", used)
	}

	store.releaseQuotaErr = errors.New("platform db unavailable")
	if err := svc.DeleteObject(ctx, testProjX, "assets", "a.txt"); err == nil {
		t.Fatal("a failed release must not report success")
	}

	store.releaseQuotaErr = nil
	if err := svc.DeleteObject(ctx, testProjX, "assets", "a.txt"); err != nil {
		t.Fatalf("retry: %v", err)
	}
	if used, _ := store.GetQuotaBytes(ctx, testProjX); used != 0 {
		t.Fatalf("retry must converge on the exact quota: got %d, want 0", used)
	}
}

// Same guarantee for the bucket cascade.
func TestService_DeleteBucket_RetryReleasesQuotaExactlyOnce(t *testing.T) {
	store := newErrStore()
	blobs := newFakeObjectStore()
	svc := NewServiceWithObjectStore(store, blobs, nil)
	ctx := context.Background()
	seedBucket(t, svc, blobs, "assets", "a.txt", 300)

	store.releaseQuotaErr = errors.New("platform db unavailable")
	if err := svc.DeleteBucket(ctx, testProjX, "assets"); err == nil {
		t.Fatal("a failed release must not report success")
	}

	store.releaseQuotaErr = nil
	if err := svc.DeleteBucket(ctx, testProjX, "assets"); err != nil {
		t.Fatalf("retry: %v", err)
	}
	if used, _ := store.GetQuotaBytes(ctx, testProjX); used != 0 {
		t.Fatalf("retry must converge on the exact quota: got %d, want 0", used)
	}
	if b, _ := store.GetBucket(ctx, testProjX, "assets"); b != nil {
		t.Error("bucket record should be gone after the successful retry")
	}
}

// EXC-404 follow-up, finding 4 — a negative size would credit the project's
// quota, so it is refused wherever a size is accepted.
func TestService_RejectsNegativeSizes(t *testing.T) {
	store := newMemStore()
	blobs := newFakeObjectStore()
	svc := NewServiceWithObjectStore(store, blobs, nil)
	ctx := context.Background()
	_, _ = svc.CreateBucket(ctx, testProjX, CreateBucketRequest{Name: "assets"})

	if _, err := svc.SignUploadURL(ctx, testProjX, "assets", "FREE", UploadURLRequest{Key: "k", Size: -1}); err == nil {
		t.Error("SignUploadURL must refuse a negative size")
	}
	blobs.putAt(testProjX, bucketOf(t, store, "assets"), stagingObjectKey(testUploadID("k")), -100, "text/plain", time.Now().UTC())
	if _, err := svc.ConfirmUpload(ctx, testProjX, "assets", "FREE", "u", ConfirmUploadRequest{Key: "k", UploadID: testUploadID("k")}); err == nil {
		t.Error("ConfirmUpload must refuse an object the store reports with a negative size")
	}
	if used, _ := store.GetQuotaBytes(ctx, testProjX); used != 0 {
		t.Errorf("a negative size must never credit the quota, got %d", used)
	}
	if _, _, err := svc.StartResumableUpload(ctx, testProjX, "assets", "FREE", UploadURLRequest{Key: "k", Size: -1}); err == nil {
		t.Error("StartResumableUpload must refuse a negative size")
	}
}

// bucketOf resolves a bucket's id, the identity the blob plane is keyed by.
func bucketOf(t *testing.T, store BucketStore, name string) string {
	t.Helper()
	bucket, err := store.GetBucket(context.Background(), testProjX, name)
	if err != nil || bucket == nil {
		t.Fatalf("GetBucket %q: %v", name, err)
	}
	return bucket.ID
}
