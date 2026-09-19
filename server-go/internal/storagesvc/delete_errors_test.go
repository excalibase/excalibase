package storagesvc

import (
	"context"
	"testing"
)

// EXC-404 — storage deletion must report what actually happened.
//
// The scenario from the finding: the object store rejects every delete.
// Bucket deletion used to ignore those errors and drop the bucket record
// anyway, so the API reported success while the bytes stayed in R2 with
// nothing left pointing at them.
func TestService_DeleteBucket_FailedObjectDeleteKeepsBucket(t *testing.T) {
	store := newMemStore()
	svc := NewService(store, newTestR2(t), nil) // closed port: every R2 call fails
	ctx := context.Background()
	_, _ = svc.CreateBucket(ctx, testProjX, CreateBucketRequest{Name: "assets"})
	bucket, _ := store.GetBucket(ctx, testProjX, "assets")
	_ = store.CreateObject(ctx, &Object{ID: "obj_1", BucketID: bucket.ID, Key: "a.txt", Size: 10})

	if err := svc.DeleteBucket(ctx, testProjX, "assets"); err == nil {
		t.Fatal("DeleteBucket must fail when the object store rejects the delete")
	}
	still, err := store.GetBucket(ctx, testProjX, "assets")
	if err != nil {
		t.Fatalf("GetBucket: %v", err)
	}
	if still == nil {
		t.Fatal("bucket record was dropped despite the failed object delete; a retry can no longer find the bytes")
	}
}

// A bucket the catalogue believes is empty is not proof the object store is
// empty: an upload that was never confirmed leaves bytes with no row. The
// bucket record may only go once the object store is observed empty.
func TestService_DeleteBucket_VerifiesObjectStoreIsEmpty(t *testing.T) {
	store := newMemStore()
	svc := NewService(store, newTestR2(t), nil)
	ctx := context.Background()
	_, _ = svc.CreateBucket(ctx, testProjX, CreateBucketRequest{Name: "assets"})

	if err := svc.DeleteBucket(ctx, testProjX, "assets"); err == nil {
		t.Fatal("DeleteBucket must fail when the object store cannot be listed to confirm it is empty")
	}
	still, _ := store.GetBucket(ctx, testProjX, "assets")
	if still == nil {
		t.Fatal("bucket record dropped without ever observing the object store")
	}
}

// DeleteObject must not report success when the catalogue row survives.
func TestService_DeleteObject_ReportsObjectStoreFailure(t *testing.T) {
	store := newMemStore()
	svc := NewService(store, newTestR2(t), nil)
	ctx := context.Background()
	_, _ = svc.CreateBucket(ctx, testProjX, CreateBucketRequest{Name: "assets"})
	bucket, _ := store.GetBucket(ctx, testProjX, "assets")
	_ = store.CreateObject(ctx, &Object{ID: "obj_1", BucketID: bucket.ID, Key: "a.txt", Size: 10})

	if err := svc.DeleteObject(ctx, testProjX, "assets", "a.txt"); err == nil {
		t.Fatal("DeleteObject must fail when the object store rejects the delete")
	}
	obj, _ := store.GetObject(ctx, bucket.ID, "a.txt")
	if obj == nil {
		t.Fatal("catalogue row removed although the bytes are still there")
	}
}
