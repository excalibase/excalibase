package storagesvc

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// EXC-405 follow-up, finding 1 — a refused confirmation used to delete the
// key it was refusing, which on an overwrite is the previous, confirmed,
// charged object: its bytes gone, its row and quota charge left behind, every
// download a 404. Uploads therefore never land on the live key. They land on
// a staging key of their own and are copied across only once they have been
// read back and accepted.

func stagedService(t *testing.T, quota map[string]int64) (*Service, *memStore, *fakeObjectStore, *Bucket) {
	t.Helper()
	store := newMemStore()
	blobs := newFakeObjectStore()
	svc := NewServiceWithObjectStore(store, blobs, quota)
	bucket, err := svc.CreateBucket(context.Background(), testProjX, CreateBucketRequest{Name: "files"})
	if err != nil {
		t.Fatalf("CreateBucket: %v", err)
	}
	return svc, store, blobs, bucket
}

// confirmStaged runs the whole protocol: ask for a URL, put the bytes where
// the URL points, confirm by upload id.
func confirmStaged(t *testing.T, svc *Service, blobs *fakeObjectStore, bucket *Bucket, key, mimeType string, size int64) (*Object, error) {
	t.Helper()
	out, err := svc.SignUploadURL(context.Background(), testProjX, bucket.Name, "FREE", UploadURLRequest{
		Key: key, MimeType: mimeType, Size: size,
	})
	if err != nil {
		return nil, err
	}
	if out.UploadID == "" {
		t.Fatal("an upload URL must name the staging upload it authorises")
	}
	blobs.put(testProjX, bucket.ID, stagingObjectKey(out.UploadID), size, mimeType)
	return svc.ConfirmUpload(context.Background(), testProjX, bucket.Name, "FREE", "u", ConfirmUploadRequest{
		Key: key, UploadID: out.UploadID,
	})
}

// The scenario in full: a good object, then an overwrite that the bucket
// refuses. The first object must still be there afterwards.
func TestService_RefusedConfirm_LeavesThePreviousObjectIntact(t *testing.T) {
	svc, store, blobs, bucket := stagedService(t, map[string]int64{"free": 1000})
	ctx := context.Background()

	if _, err := confirmStaged(t, svc, blobs, bucket, "report.txt", "text/plain", 100); err != nil {
		t.Fatalf("first confirm: %v", err)
	}
	if !blobs.has(testProjX, bucket.ID, "report.txt") {
		t.Fatal("the confirmed object should be stored under its own key")
	}

	// An overwrite far over the project's quota: refused.
	out, err := svc.SignUploadURL(ctx, testProjX, "files", "FREE", UploadURLRequest{
		Key: "report.txt", MimeType: "text/plain", Size: 5000,
	})
	if err != nil {
		// Refused before a URL was even issued — the live key is untouched
		// either way, which is the point.
		if !blobs.has(testProjX, bucket.ID, "report.txt") {
			t.Fatal("the previous object was destroyed by a refused upload")
		}
		return
	}
	blobs.put(testProjX, bucket.ID, stagingObjectKey(out.UploadID), 5000, "text/plain")
	if _, err := svc.ConfirmUpload(ctx, testProjX, "files", "FREE", "u", ConfirmUploadRequest{
		Key: "report.txt", UploadID: out.UploadID,
	}); err == nil {
		t.Fatal("an over-quota overwrite must be refused")
	}

	if !blobs.has(testProjX, bucket.ID, "report.txt") {
		t.Fatal("the previous confirmed object was destroyed by the refused overwrite")
	}
	obj, _ := store.GetObject(ctx, bucket.ID, "report.txt")
	if obj == nil || obj.Size != 100 {
		t.Fatalf("the previous row must still describe the stored object, got %+v", obj)
	}
	if used, _ := store.GetQuotaBytes(ctx, testProjX); used != 100 {
		t.Errorf("quota after the refusal: got %d, want the original 100", used)
	}
}

// A refusal takes the staged bytes with it: they were never anybody's object.
func TestService_RefusedConfirm_DeletesOnlyTheStagedObject(t *testing.T) {
	svc, _, blobs, bucket := stagedService(t, nil)
	ctx := context.Background()
	if _, err := confirmStaged(t, svc, blobs, bucket, "logo.png", "image/png", 10); err != nil {
		t.Fatalf("first confirm: %v", err)
	}
	// Re-create the bucket's allow-list by hand: only PNG is allowed.
	stored, _ := svc.store.GetBucket(ctx, testProjX, "files")
	stored.AllowedTypes = []string{"image/png"}

	out, err := svc.SignUploadURL(ctx, testProjX, "files", "FREE", UploadURLRequest{
		Key: "logo.png", MimeType: "image/png", Size: 10,
	})
	if err != nil {
		t.Fatalf("SignUploadURL: %v", err)
	}
	// What actually landed is not a PNG.
	blobs.put(testProjX, bucket.ID, stagingObjectKey(out.UploadID), 10, "application/x-msdownload")
	if _, err := svc.ConfirmUpload(ctx, testProjX, "files", "FREE", "u", ConfirmUploadRequest{
		Key: "logo.png", UploadID: out.UploadID,
	}); err == nil {
		t.Fatal("a disallowed stored type must be refused")
	}
	if blobs.has(testProjX, bucket.ID, stagingObjectKey(out.UploadID)) {
		t.Error("the staged object must be deleted when its confirmation is refused")
	}
	if !blobs.has(testProjX, bucket.ID, "logo.png") {
		t.Error("the previous confirmed object must survive")
	}
}

// An accepted confirmation moves the bytes onto the live key and clears the
// staging copy, so nothing is left paying for two copies.
func TestService_ConfirmUpload_CopiesStagedObjectOntoTheKey(t *testing.T) {
	svc, store, blobs, bucket := stagedService(t, nil)
	ctx := context.Background()
	out, err := svc.SignUploadURL(ctx, testProjX, "files", "FREE", UploadURLRequest{
		Key: "docs/a.txt", MimeType: "text/plain", Size: 42,
	})
	if err != nil {
		t.Fatalf("SignUploadURL: %v", err)
	}
	if !strings.Contains(out.URL, stagingPrefix) {
		t.Errorf("the signed PUT must address the staging namespace: %s", out.URL)
	}
	blobs.put(testProjX, bucket.ID, stagingObjectKey(out.UploadID), 42, "text/plain")

	obj, err := svc.ConfirmUpload(ctx, testProjX, "files", "FREE", "u", ConfirmUploadRequest{
		Key: "docs/a.txt", UploadID: out.UploadID,
	})
	if err != nil {
		t.Fatalf("ConfirmUpload: %v", err)
	}
	if obj.Size != 42 {
		t.Errorf("recorded size: got %d, want 42", obj.Size)
	}
	if !blobs.has(testProjX, bucket.ID, "docs/a.txt") {
		t.Error("the object should now be stored under its own key")
	}
	if blobs.has(testProjX, bucket.ID, stagingObjectKey(out.UploadID)) {
		t.Error("the staging copy should be gone once the object is recorded")
	}
	if used, _ := store.GetQuotaBytes(ctx, testProjX); used != 42 {
		t.Errorf("quota: got %d, want 42", used)
	}
}

// An upload id nobody staged is not something to confirm.
func TestService_ConfirmUpload_UnknownUploadIDIsNotFound(t *testing.T) {
	svc, _, _, _ := stagedService(t, nil)
	_, err := svc.ConfirmUpload(context.Background(), testProjX, "files", "FREE", "u", ConfirmUploadRequest{
		Key: "a.txt", UploadID: "upl_nobody_staged_this",
	})
	if !errors.Is(err, ErrObjectNotFound) {
		t.Fatalf("want an object-not-found, got %v", err)
	}
}

// A confirmation that does not name an upload is not a confirmation.
func TestService_ConfirmUpload_RequiresAnUploadID(t *testing.T) {
	svc, _, _, _ := stagedService(t, nil)
	_, err := svc.ConfirmUpload(context.Background(), testProjX, "files", "FREE", "u", ConfirmUploadRequest{
		Key: "a.txt",
	})
	var invalid *ValidationError
	if !errors.As(err, &invalid) {
		t.Fatalf("want a validation error naming the missing upload id, got %v", err)
	}
}

// A crash between the copy and the record is recoverable: the staged object
// is only deleted after the row is written, so the same confirmation run
// again completes.
func TestService_ConfirmUpload_RetryAfterAFailedRecordCompletes(t *testing.T) {
	store := newErrStore()
	blobs := newFakeObjectStore()
	svc := NewServiceWithObjectStore(store, blobs, nil)
	ctx := context.Background()
	bucket, _ := svc.CreateBucket(ctx, testProjX, CreateBucketRequest{Name: "files"})

	out, err := svc.SignUploadURL(ctx, testProjX, "files", "FREE", UploadURLRequest{
		Key: "a.txt", MimeType: "text/plain", Size: 10,
	})
	if err != nil {
		t.Fatalf("SignUploadURL: %v", err)
	}
	blobs.put(testProjX, bucket.ID, stagingObjectKey(out.UploadID), 10, "text/plain")

	store.recordObjectErr = errors.New("platform db unavailable")
	if _, err := svc.ConfirmUpload(ctx, testProjX, "files", "FREE", "u", ConfirmUploadRequest{
		Key: "a.txt", UploadID: out.UploadID,
	}); err == nil {
		t.Fatal("a failed record must be reported")
	}
	if !blobs.has(testProjX, bucket.ID, stagingObjectKey(out.UploadID)) {
		t.Fatal("the staged object must survive a failed record so the retry can finish")
	}

	store.recordObjectErr = nil
	if _, err := svc.ConfirmUpload(ctx, testProjX, "files", "FREE", "u", ConfirmUploadRequest{
		Key: "a.txt", UploadID: out.UploadID,
	}); err != nil {
		t.Fatalf("retry: %v", err)
	}
	if used, _ := store.GetQuotaBytes(ctx, testProjX); used != 10 {
		t.Errorf("quota after the retry: got %d, want 10", used)
	}
	if blobs.has(testProjX, bucket.ID, stagingObjectKey(out.UploadID)) {
		t.Error("the staging copy should be gone after the successful retry")
	}
}

// Resumable uploads stage the same way, so the same guarantee holds for them.
func TestService_StartResumableUpload_TargetsTheStagingNamespace(t *testing.T) {
	svc, _, _, bucket := stagedService(t, nil)
	storeKey, uploadID, err := svc.StartResumableUpload(context.Background(), testProjX, "files", "FREE", UploadURLRequest{
		Key: "clip.mp4", MimeType: "video/mp4", Size: 10,
	})
	if err != nil {
		t.Fatalf("StartResumableUpload: %v", err)
	}
	if uploadID == "" {
		t.Fatal("a resumable upload must name the staging upload it created")
	}
	if !strings.Contains(storeKey, stagingPrefix) || !strings.Contains(storeKey, bucket.ID) {
		t.Errorf("resumable uploads must land in the bucket's staging namespace, got %s", storeKey)
	}
	if strings.Contains(storeKey, "clip.mp4") {
		t.Errorf("a resumable upload must not land on the live key: %s", storeKey)
	}
}

// A copy that fails leaves the object's key alone and the staged bytes in
// place, so the same confirmation run again can still finish.
func TestService_ConfirmUpload_ReportsCopyFailure(t *testing.T) {
	svc, _, blobs, bucket := stagedService(t, nil)
	ctx := context.Background()
	out, err := svc.SignUploadURL(ctx, testProjX, "files", "FREE", UploadURLRequest{
		Key: "a.txt", MimeType: "text/plain", Size: 10,
	})
	if err != nil {
		t.Fatalf("SignUploadURL: %v", err)
	}
	blobs.put(testProjX, bucket.ID, stagingObjectKey(out.UploadID), 10, "text/plain")
	blobs.copyErr = errors.New("object store refuses copies")

	_, err = svc.ConfirmUpload(ctx, testProjX, "files", "FREE", "u", ConfirmUploadRequest{
		Key: "a.txt", UploadID: out.UploadID,
	})
	if err == nil || !strings.Contains(err.Error(), "store uploaded object") {
		t.Fatalf("a failed copy must surface, got %v", err)
	}
	if blobs.has(testProjX, bucket.ID, "a.txt") {
		t.Error("nothing should have landed on the key")
	}
	if !blobs.has(testProjX, bucket.ID, stagingObjectKey(out.UploadID)) {
		t.Error("the staged bytes must survive so the retry can finish")
	}
}

// An object too large to be moved onto its key in one copy is refused rather
// than left staged forever.
func TestService_ConfirmUpload_RefusesAnObjectTooLargeToCopy(t *testing.T) {
	svc, _, blobs, bucket := stagedService(t, nil)
	ctx := context.Background()
	blobs.put(testProjX, bucket.ID, stagingObjectKey("upl_huge"), MaxSingleCopyBytes+1, "application/octet-stream")

	_, err := svc.ConfirmUpload(ctx, testProjX, "files", "FREE", "u", ConfirmUploadRequest{
		Key: "huge.bin", UploadID: "upl_huge",
	})
	if err == nil || !strings.Contains(err.Error(), "single upload") {
		t.Fatalf("want a size refusal, got %v", err)
	}
	if blobs.has(testProjX, bucket.ID, stagingObjectKey("upl_huge")) {
		t.Error("the refused upload must be cleared")
	}
}

// A staged upload whose bytes cannot be cleared after a successful record is
// reported: the object is stored, but something is left behind to say so.
func TestService_ConfirmUpload_ReportsStagingCleanupFailure(t *testing.T) {
	svc, store, blobs, bucket := stagedService(t, nil)
	ctx := context.Background()
	blobs.put(testProjX, bucket.ID, stagingObjectKey("upl_1"), 10, "text/plain")
	blobs.deleteErr = errors.New("object store refuses deletes")

	_, err := svc.ConfirmUpload(ctx, testProjX, "files", "FREE", "u", ConfirmUploadRequest{
		Key: "a.txt", UploadID: "upl_1",
	})
	if err == nil || !strings.Contains(err.Error(), "clear staged upload") {
		t.Fatalf("want a cleanup failure, got %v", err)
	}
	// The object itself was recorded before the cleanup was attempted.
	if row, _ := store.GetObject(ctx, bucket.ID, "a.txt"); row == nil {
		t.Error("the object should already be recorded")
	}
}

// Resumable uploads are refused for the same reasons a presigned one is.
func TestService_StartResumableUpload_RefusesAnUnusableRequest(t *testing.T) {
	svc, _, _, _ := stagedService(t, nil)
	ctx := context.Background()
	if _, _, err := svc.StartResumableUpload(ctx, testProjX, "ghost", "FREE", UploadURLRequest{
		Key: "a.txt", MimeType: "text/plain", Size: 1,
	}); err == nil {
		t.Error("an unknown bucket must be refused")
	}
	if _, _, err := svc.StartResumableUpload(ctx, testProjX, "files", "FREE", UploadURLRequest{
		Key: "a.txt", MimeType: "text/plain", Size: 0,
	}); err == nil {
		t.Error("an upload with no declared length must be refused")
	}
}
