package storagesvc

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"
)

// EXC-419 — deleting a project drops its catalogue rows but leaves the bytes.
// Once the row is gone nothing names them: no bucket, no project, no listing.
// They are unreachable and billed forever, so the deletion has to clear the
// project's own object-store prefix itself.

func purgeableService(t *testing.T) (*Service, *memStore, *fakeObjectStore) {
	t.Helper()
	store := newMemStore()
	blobs := newFakeObjectStore()
	return NewServiceWithObjectStore(store, blobs, nil), store, blobs
}

// Both halves go: the objects a confirm recorded, and the ones an upload left
// behind without ever confirming.
func TestService_PurgeProjectObjects_RemovesConfirmedAndUnconfirmed(t *testing.T) {
	svc, store, blobs := purgeableService(t)
	ctx := context.Background()
	bucket, err := svc.CreateBucket(ctx, testProjX, CreateBucketRequest{Name: "files"})
	if err != nil {
		t.Fatalf("CreateBucket: %v", err)
	}
	blobs.put(testProjX, bucket.ID, stagingObjectKey("upl_recorded"), 10, "text/plain")
	if _, err := svc.ConfirmUpload(ctx, testProjX, "files", "FREE", "u", ConfirmUploadRequest{
		Key: "recorded.txt", UploadID: "upl_recorded",
	}); err != nil {
		t.Fatalf("ConfirmUpload: %v", err)
	}
	// Bytes from an upload nobody confirmed: no row names them, and they sit
	// in the staging namespace until something clears it.
	blobs.put(testProjX, bucket.ID, stagingObjectKey("upl_abandoned"), 10, "application/octet-stream")

	deleted, err := svc.PurgeProjectObjects(ctx, testProjX)
	if err != nil {
		t.Fatalf("PurgeProjectObjects: %v", err)
	}
	if deleted != 2 {
		t.Errorf("deleted %d objects, want both", deleted)
	}
	if blobs.has(testProjX, bucket.ID, "recorded.txt") {
		t.Error("a confirmed object survived the project's deletion")
	}
	if blobs.has(testProjX, bucket.ID, stagingObjectKey("upl_abandoned")) {
		t.Error("a staged upload survived the project's deletion")
	}
	_ = store
}

// The prefix is the project's own: "proj-x" must never reach "proj-xtra".
func TestService_PurgeProjectObjects_LeavesNeighbouringPrefixAlone(t *testing.T) {
	svc, _, blobs := purgeableService(t)
	ctx := context.Background()
	mine, _ := svc.CreateBucket(ctx, testProjX, CreateBucketRequest{Name: "files"})
	neighbour, _ := svc.CreateBucket(ctx, testProjX+"tra", CreateBucketRequest{Name: "files"})
	blobs.put(testProjX, mine.ID, "mine.bin", 10, "text/plain")
	blobs.put(testProjX+"tra", neighbour.ID, "theirs.bin", 10, "text/plain")

	deleted, err := svc.PurgeProjectObjects(ctx, testProjX)
	if err != nil {
		t.Fatalf("PurgeProjectObjects: %v", err)
	}
	if deleted != 1 {
		t.Errorf("deleted %d objects, want only this project's one", deleted)
	}
	if !blobs.has(testProjX+"tra", neighbour.ID, "theirs.bin") {
		t.Fatal("a neighbouring project's object was deleted")
	}
}

// A purge that cannot finish says so, so the deletion stops before the record
// that names the prefix is removed.
func TestService_PurgeProjectObjects_ReportsFailure(t *testing.T) {
	svc, _, blobs := purgeableService(t)
	ctx := context.Background()
	bucket, _ := svc.CreateBucket(ctx, testProjX, CreateBucketRequest{Name: "files"})
	blobs.put(testProjX, bucket.ID, "a.bin", 10, "text/plain")
	blobs.deleteErr = errors.New("object store refuses deletes")

	if _, err := svc.PurgeProjectObjects(ctx, testProjX); err == nil {
		t.Fatal("a failed purge must not report success")
	}
}

// Running it again on a project that is already clear is success with nothing
// to do, so a retried deletion resumes rather than failing forever.
func TestService_PurgeProjectObjects_IsIdempotent(t *testing.T) {
	svc, _, blobs := purgeableService(t)
	ctx := context.Background()
	bucket, _ := svc.CreateBucket(ctx, testProjX, CreateBucketRequest{Name: "files"})
	blobs.put(testProjX, bucket.ID, "a.bin", 10, "text/plain")

	if _, err := svc.PurgeProjectObjects(ctx, testProjX); err != nil {
		t.Fatalf("first purge: %v", err)
	}
	deleted, err := svc.PurgeProjectObjects(ctx, testProjX)
	if err != nil {
		t.Fatalf("second purge: %v", err)
	}
	if deleted != 0 {
		t.Errorf("a clear project has nothing to delete, got %d", deleted)
	}
}

// A project id that could reach outside its own prefix is refused before
// anything is listed, let alone deleted.
func TestService_PurgeProjectObjects_RefusesUnsafeProjectID(t *testing.T) {
	svc, _, _ := purgeableService(t)
	for _, id := range []string{"", "../other", "a/b"} {
		if _, err := svc.PurgeProjectObjects(context.Background(), id); err == nil {
			t.Errorf("project id %q must be refused", id)
		}
	}
}

// The prefix carries a trailing slash, which is what keeps one project id
// from matching another that merely starts with it.
func TestProjectPrefix(t *testing.T) {
	got, err := projectPrefix("proj-x")
	if err != nil {
		t.Fatalf("projectPrefix: %v", err)
	}
	if want := "projects/proj-x/"; got != want {
		t.Fatalf("projectPrefix: got %q, want %q", got, want)
	}
	neighbour, err := projectPrefix("proj-xtra")
	if err != nil {
		t.Fatalf("projectPrefix: %v", err)
	}
	if strings.HasPrefix(neighbour, got) {
		t.Errorf("%q must not fall under %q", neighbour, got)
	}
}

// With no object store wired the purge says so rather than reporting that it
// cleared a project it never looked at.
func TestService_PurgeProjectObjects_RequiresObjectStore(t *testing.T) {
	svc := NewService(newMemStore(), nil, nil)
	if _, err := svc.PurgeProjectObjects(context.Background(), testProjX); err == nil {
		t.Fatal("a purge with no object store must not report success")
	}
}

// A listing that cannot be taken is not an empty project.
func TestService_PurgeProjectObjects_ReportsListFailure(t *testing.T) {
	svc, _, blobs := purgeableService(t)
	blobs.listErr = errors.New("object store unreachable")
	if _, err := svc.PurgeProjectObjects(context.Background(), testProjX); err == nil {
		t.Fatal("a failed listing must not read as a clear project")
	}
}

// More objects than fit in one listing still all go.
func TestService_PurgeProjectObjects_WalksEveryPage(t *testing.T) {
	svc, _, blobs := purgeableService(t)
	ctx := context.Background()
	bucket, _ := svc.CreateBucket(ctx, testProjX, CreateBucketRequest{Name: "files"})
	const total = projectPurgePageSize + 25
	for i := 0; i < total; i++ {
		blobs.put(testProjX, bucket.ID, "obj-"+strconv.Itoa(i), 1, "text/plain")
	}

	deleted, err := svc.PurgeProjectObjects(ctx, testProjX)
	if err != nil {
		t.Fatalf("PurgeProjectObjects: %v", err)
	}
	if deleted != total {
		t.Errorf("deleted %d, want %d", deleted, total)
	}
	left, _ := blobs.ListKeysWithPrefix(ctx, "projects/"+testProjX+"/", 10)
	if len(left) != 0 {
		t.Errorf("objects left behind: %v", left)
	}
}

// A store that accepts deletes without removing anything is a fault, not a
// reason to list the same page forever.
func TestService_PurgeProjectObjects_StopsWhenDeletesDoNothing(t *testing.T) {
	svc, _, blobs := purgeableService(t)
	ctx := context.Background()
	bucket, _ := svc.CreateBucket(ctx, testProjX, CreateBucketRequest{Name: "files"})
	blobs.put(testProjX, bucket.ID, "stuck.bin", 10, "text/plain")
	blobs.ignoreDeletes = true

	_, err := svc.PurgeProjectObjects(ctx, testProjX)
	if err == nil || !strings.Contains(err.Error(), "did not converge") {
		t.Fatalf("want a non-convergence failure, got %v", err)
	}
}
