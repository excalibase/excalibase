package service

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/storagesvc"
)

// EXC-419 end to end — the deletion is wired to the real storage service over
// an in-memory blob plane, so the assertion is on what is actually left after
// a project is deleted rather than on a stub being called.

// memoryBlobPlane is a storagesvc.ObjectStore holding whole store keys.
type memoryBlobPlane struct {
	mu   sync.Mutex
	keys map[string]bool
}

func newMemoryBlobPlane() *memoryBlobPlane {
	return &memoryBlobPlane{keys: map[string]bool{}}
}

func (m *memoryBlobPlane) put(key string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.keys[key] = true
}

func (m *memoryBlobPlane) keysUnder(prefix string) []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []string{}
	for k := range m.keys {
		if strings.HasPrefix(k, prefix) {
			out = append(out, k)
		}
	}
	return out
}

func (m *memoryBlobPlane) storeKey(projectID, bucketID, key string) string {
	return "projects/" + projectID + "/buckets/" + bucketID + "/" + key
}

func (m *memoryBlobPlane) SignedPutURL(_ context.Context, projectID, bucketID, key, _ string, ttl time.Duration) (string, time.Time, error) {
	return "https://blob/" + m.storeKey(projectID, bucketID, key), time.Now().Add(ttl), nil
}

func (m *memoryBlobPlane) SignedGetURL(_ context.Context, projectID, bucketID, key string, ttl time.Duration) (string, time.Time, error) {
	return "https://blob/" + m.storeKey(projectID, bucketID, key), time.Now().Add(ttl), nil
}

func (m *memoryBlobPlane) PublicURL(projectID, bucketID, key string) (string, error) {
	return "https://blob/" + m.storeKey(projectID, bucketID, key), nil
}

func (m *memoryBlobPlane) DeleteObject(ctx context.Context, projectID, bucketID, key string) error {
	return m.DeleteKey(ctx, "projects/", m.storeKey(projectID, bucketID, key))
}

func (m *memoryBlobPlane) ListObjectKeys(_ context.Context, projectID, bucketID string, limit int32) ([]string, error) {
	return m.keysUnder("projects/" + projectID + "/buckets/" + bucketID + "/"), nil
}

func (m *memoryBlobPlane) ListKeysWithPrefix(_ context.Context, prefix string, limit int32) ([]string, error) {
	keys := m.keysUnder(prefix)
	if limit > 0 && int32(len(keys)) > limit {
		keys = keys[:limit]
	}
	return keys, nil
}

func (m *memoryBlobPlane) DeleteKey(_ context.Context, prefix, key string) error {
	if !strings.HasPrefix(key, prefix) {
		return errors.New("key outside the caller's prefix")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.keys, key)
	return nil
}

// After a full deletion nothing is left under the project's prefix — neither
// the objects its catalogue named nor the uploads it never confirmed — and a
// neighbouring project whose id merely starts the same is untouched.
func TestDeprovision_LeavesNothingUnderTheProjectPrefix(t *testing.T) {
	svc, store, _ := setupDeletionTest(t)
	blobs := newMemoryBlobPlane()
	storage := storagesvc.NewServiceWithObjectStore(newDeletionBucketStore(), blobs, nil)
	svc.SetObjectPurger(storage)

	blobs.put("projects/" + testDeletingProj + "/buckets/bkt_1/recorded.txt")
	blobs.put("projects/" + testDeletingProj + "/buckets/bkt_1/never-confirmed.bin")
	blobs.put("projects/" + testDeletingProj + "tra/buckets/bkt_9/theirs.bin")

	if err := svc.Deprovision(context.Background(), testDeletingProj); err != nil {
		t.Fatalf("Deprovision: %v", err)
	}
	if left := blobs.keysUnder("projects/" + testDeletingProj + "/"); len(left) != 0 {
		t.Errorf("objects left under the deleted project's prefix: %v", left)
	}
	if left := blobs.keysUnder("projects/" + testDeletingProj + "tra/"); len(left) != 1 {
		t.Errorf("a neighbouring project's objects were taken: %v", left)
	}
	if inst, _ := store.FindByProjectID(testDeletingProj); inst != nil {
		t.Error("the record should be gone")
	}
}

// An object store that cannot be cleared stops the deletion before the record
// goes, and a retry finishes it.
func TestDeprovision_ObjectStoreFailureKeepsTheRecordUntilItClears(t *testing.T) {
	svc, store, _ := setupDeletionTest(t)
	blobs := &refusingBlobPlane{memoryBlobPlane: newMemoryBlobPlane()}
	storage := storagesvc.NewServiceWithObjectStore(newDeletionBucketStore(), blobs, nil)
	svc.SetObjectPurger(storage)
	blobs.put("projects/" + testDeletingProj + "/buckets/bkt_1/a.bin")
	blobs.refuse = true

	if err := svc.Deprovision(context.Background(), testDeletingProj); err == nil {
		t.Fatal(testWantErrNil)
	}
	inst := requireDeletingRow(t, store, "DELETE_PROJECT_OBJECTS")
	if inst == nil {
		t.Fatal("the row must survive so the purge can be retried")
	}

	blobs.refuse = false
	if err := svc.Deprovision(context.Background(), testDeletingProj); err != nil {
		t.Fatalf("retry: %v", err)
	}
	if left := blobs.keysUnder("projects/" + testDeletingProj + "/"); len(left) != 0 {
		t.Errorf("objects left after the retry: %v", left)
	}
	if inst, _ := store.FindByProjectID(testDeletingProj); inst != nil {
		t.Error("the record should be gone after the retry")
	}
}

// refusingBlobPlane is the in-memory plane with deletes turned off.
type refusingBlobPlane struct {
	*memoryBlobPlane
	refuse bool
}

func (r *refusingBlobPlane) DeleteKey(ctx context.Context, prefix, key string) error {
	if r.refuse {
		return errors.New("object store unreachable")
	}
	return r.memoryBlobPlane.DeleteKey(ctx, prefix, key)
}

// newDeletionBucketStore is an empty catalogue: the purge works from the
// object store's prefix, not from rows, which is the whole point — by the
// time a project is deleted its rows are going too.
func newDeletionBucketStore() storagesvc.BucketStore { return emptyBucketStore{} }

type emptyBucketStore struct{}

func (emptyBucketStore) CreateBucket(context.Context, *storagesvc.Bucket) error { return nil }
func (emptyBucketStore) GetBucket(context.Context, string, string) (*storagesvc.Bucket, error) {
	return nil, nil
}
func (emptyBucketStore) ListBuckets(context.Context, string) ([]storagesvc.Bucket, error) {
	return nil, nil
}
func (emptyBucketStore) ListAllBuckets(context.Context) ([]storagesvc.Bucket, error) {
	return nil, nil
}
func (emptyBucketStore) DeleteBucket(context.Context, string, string) error { return nil }
func (emptyBucketStore) SetBucketStatus(context.Context, string, string, string) error {
	return nil
}
func (emptyBucketStore) CreateObject(context.Context, *storagesvc.Object) error { return nil }
func (emptyBucketStore) RecordObjectWithinQuota(context.Context, string, *storagesvc.Object, int64) (bool, error) {
	return true, nil
}
func (emptyBucketStore) GetObject(context.Context, string, string) (*storagesvc.Object, error) {
	return nil, nil
}
func (emptyBucketStore) ListObjects(context.Context, string, string, int, string) ([]storagesvc.Object, string, error) {
	return nil, "", nil
}
func (emptyBucketStore) DeleteObjectAndReleaseQuota(context.Context, string, string, string) (bool, error) {
	return false, nil
}
func (emptyBucketStore) GetQuotaBytes(context.Context, string) (int64, error) { return 0, nil }
func (emptyBucketStore) AddQuotaBytes(context.Context, string, int64) error   { return nil }
