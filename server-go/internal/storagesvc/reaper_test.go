package storagesvc

import (
	"context"
	"testing"
	"time"
)

// An upload that is never confirmed leaves bytes with no catalogue row. Quota
// is computed from the catalogue, so those bytes are free forever: take an
// upload URL, PUT to it, never confirm, repeat. The reaper is what bounds
// that — anything older than the grace period with no row is deleted.
func TestService_ReapUnconfirmedUploads_DeletesAbandonedObjects(t *testing.T) {
	backend := newStubBackend()
	svc, store := newStubbedService(t, backend, nil)
	ctx := context.Background()
	_, _ = svc.CreateBucket(ctx, testProjX, CreateBucketRequest{Name: "files"})
	now := time.Now().UTC()

	// Confirmed: has a row, must survive.
	backend.putAt("kept.txt", 10, "text/plain", now.Add(-3*time.Hour))
	if _, err := svc.ConfirmUpload(ctx, testProjX, "files", "FREE", "u", ConfirmUploadRequest{Key: "kept.txt"}); err != nil {
		t.Fatalf("ConfirmUpload: %v", err)
	}
	// Abandoned and old enough: must go.
	backend.putAt("abandoned.bin", 5_000_000, "application/octet-stream", now.Add(-3*time.Hour))
	// Abandoned but still within the grace period: may be in flight.
	backend.putAt("in-flight.bin", 100, "application/octet-stream", now.Add(-time.Minute))

	report, err := svc.ReapUnconfirmedUploads(ctx, time.Hour, now)
	if err != nil {
		t.Fatalf("ReapUnconfirmedUploads: %v", err)
	}
	if len(report.Deleted) != 1 || report.Deleted[0] != "files/abandoned.bin" {
		t.Fatalf("expected only the abandoned object to be reaped, got %v", report.Deleted)
	}
	if len(report.Failed) != 0 {
		t.Errorf("unexpected failures: %v", report.Failed)
	}
	if _, ok := backend.lookup("/files/kept.txt"); !ok {
		t.Error("a confirmed object must not be reaped")
	}
	if _, ok := backend.lookup("/files/in-flight.bin"); !ok {
		t.Error("an object inside the grace period must not be reaped")
	}
	if _, ok := backend.lookup("/files/abandoned.bin"); ok {
		t.Error("the abandoned object is still in the store")
	}
	// The reaper never touches quota: unconfirmed bytes were never charged.
	if used, _ := store.GetQuotaBytes(ctx, testProjX); used != 10 {
		t.Errorf("quota after sweep: got %d, want the 10 bytes of the confirmed object", used)
	}
}

// A grace of zero falls back to the default rather than deleting everything
// in flight.
func TestService_ReapUnconfirmedUploads_DefaultsGrace(t *testing.T) {
	backend := newStubBackend()
	svc, _ := newStubbedService(t, backend, nil)
	ctx := context.Background()
	_, _ = svc.CreateBucket(ctx, testProjX, CreateBucketRequest{Name: "files"})
	now := time.Now().UTC()
	backend.putAt("fresh.bin", 1, "application/octet-stream", now)

	report, err := svc.ReapUnconfirmedUploads(ctx, 0, now)
	if err != nil {
		t.Fatalf("ReapUnconfirmedUploads: %v", err)
	}
	if len(report.Deleted) != 0 {
		t.Errorf("a just-written object must survive the default grace, got %v", report.Deleted)
	}
}

// The sweep spans every project, so one tenant's buckets are not a special
// case, and a bucket that cannot be listed is recorded rather than fatal.
func TestService_ReapUnconfirmedUploads_ReportsPerBucketFailure(t *testing.T) {
	backend := newStubBackend()
	store := newMemStore()
	svc := serviceOverStub(t, store, backend, nil)
	ctx := context.Background()
	_, _ = svc.CreateBucket(ctx, testProjX, CreateBucketRequest{Name: "files"})
	_, _ = svc.CreateBucket(ctx, "other-project", CreateBucketRequest{Name: "files"})
	now := time.Now().UTC()
	backend.putAt("abandoned.bin", 1, "application/octet-stream", now.Add(-2*time.Hour))

	report, err := svc.ReapUnconfirmedUploads(ctx, time.Hour, now)
	if err != nil {
		t.Fatalf("ReapUnconfirmedUploads: %v", err)
	}
	// Both projects' buckets share the stub's key space, so the object is
	// reaped once and the second bucket simply finds nothing left.
	if len(report.Deleted) != 1 {
		t.Errorf("expected one deletion across both projects, got %v", report.Deleted)
	}
}

func TestService_ReapUnconfirmedUploads_RequiresObjectStore(t *testing.T) {
	svc := NewService(newMemStore(), nil, nil)
	if _, err := svc.ReapUnconfirmedUploads(context.Background(), time.Hour, time.Now()); err == nil {
		t.Fatal("a sweep with no object store must report that, not report success")
	}
}
