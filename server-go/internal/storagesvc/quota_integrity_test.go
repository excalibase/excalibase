package storagesvc

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// EXC-405 follow-up — what confirm does to the quota, and what the reaper may
// take away while a confirm is in flight.

func confirmableService(t *testing.T, quota map[string]int64) (*Service, *memStore, *fakeObjectStore, *Bucket) {
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

// Finding 5 — the runtime retries a confirm it never saw the answer to, and a
// client may re-confirm a key it overwrote. Charging the full size each time
// walks the project's usage up without bound until nobody can upload at all.
func TestService_ConfirmUpload_RepeatedConfirmChargesOnce(t *testing.T) {
	svc, store, blobs, bucket := confirmableService(t, nil)
	ctx := context.Background()
	blobs.put(testProjX, bucket.ID, "a.txt", 500, "text/plain")

	for i := 0; i < 3; i++ {
		if _, err := svc.ConfirmUpload(ctx, testProjX, "files", "FREE", "u", ConfirmUploadRequest{Key: "a.txt"}); err != nil {
			t.Fatalf("confirm %d: %v", i, err)
		}
	}
	if used, _ := store.GetQuotaBytes(ctx, testProjX); used != 500 {
		t.Fatalf("three confirms of one object charged %d, want 500", used)
	}
	out, _ := svc.ListObjects(ctx, testProjX, "files", ListObjectsRequest{})
	if len(out.Objects) != 1 {
		t.Errorf("re-confirming must not create a second row, got %d", len(out.Objects))
	}
}

// An overwrite moves the usage by the difference, in both directions.
func TestService_ConfirmUpload_OverwriteChargesTheDelta(t *testing.T) {
	svc, store, blobs, bucket := confirmableService(t, nil)
	ctx := context.Background()

	blobs.put(testProjX, bucket.ID, "a.txt", 500, "text/plain")
	if _, err := svc.ConfirmUpload(ctx, testProjX, "files", "FREE", "u", ConfirmUploadRequest{Key: "a.txt"}); err != nil {
		t.Fatalf("first confirm: %v", err)
	}
	blobs.put(testProjX, bucket.ID, "a.txt", 900, "text/plain")
	if _, err := svc.ConfirmUpload(ctx, testProjX, "files", "FREE", "u", ConfirmUploadRequest{Key: "a.txt"}); err != nil {
		t.Fatalf("grow: %v", err)
	}
	if used, _ := store.GetQuotaBytes(ctx, testProjX); used != 900 {
		t.Fatalf("after growing to 900 bytes the usage is %d, want 900", used)
	}
	blobs.put(testProjX, bucket.ID, "a.txt", 100, "text/plain")
	if _, err := svc.ConfirmUpload(ctx, testProjX, "files", "FREE", "u", ConfirmUploadRequest{Key: "a.txt"}); err != nil {
		t.Fatalf("shrink: %v", err)
	}
	if used, _ := store.GetQuotaBytes(ctx, testProjX); used != 100 {
		t.Fatalf("after shrinking to 100 bytes the usage is %d, want 100", used)
	}
}

// Finding 6 — the cap is decided by the same write that spends against it.
// A confirm that lost the race is refused, and its object deleted, rather
// than both being let through because each read an under-cap total.
func TestService_ConfirmUpload_ChargeIsConditionalOnTheCap(t *testing.T) {
	svc, store, blobs, bucket := confirmableService(t, map[string]int64{"free": 1000})
	ctx := context.Background()
	blobs.put(testProjX, bucket.ID, "a.bin", 600, "application/octet-stream")
	blobs.put(testProjX, bucket.ID, "b.bin", 600, "application/octet-stream")

	if _, err := svc.ConfirmUpload(ctx, testProjX, "files", "FREE", "u", ConfirmUploadRequest{Key: "a.bin"}); err != nil {
		t.Fatalf("first confirm: %v", err)
	}
	_, err := svc.ConfirmUpload(ctx, testProjX, "files", "FREE", "u", ConfirmUploadRequest{Key: "b.bin"})
	if err == nil {
		t.Fatal("the second confirm must not be charged past the cap")
	}
	if used, _ := store.GetQuotaBytes(ctx, testProjX); used != 600 {
		t.Fatalf("usage %d exceeds the cap the writes were meant to respect", used)
	}
	if blobs.has(testProjX, bucket.ID, "b.bin") {
		t.Error("the refused object must be deleted")
	}
}

// Finding 7 — a confirm landing after the reaper decided the object was
// abandoned would leave a row with no bytes. The confirm window closes before
// the reaper's does, so the two cannot overlap.
func TestService_ConfirmUpload_RefusesAnExpiredUpload(t *testing.T) {
	svc, store, blobs, bucket := confirmableService(t, nil)
	ctx := context.Background()
	now := time.Now().UTC()
	svc.now = func() time.Time { return now }

	blobs.putAt(testProjX, bucket.ID, "stale.bin", 10, "text/plain", now.Add(-DefaultUnconfirmedGrace))
	_, err := svc.ConfirmUpload(ctx, testProjX, "files", "FREE", "u", ConfirmUploadRequest{Key: "stale.bin"})
	if err == nil || !strings.Contains(err.Error(), "expired") {
		t.Fatalf("confirming an upload older than the reaper's grace must be refused, got %v", err)
	}
	var invalid *ValidationError
	if !errors.As(err, &invalid) {
		t.Errorf("an expired upload is the caller's to retry, got %T", err)
	}
	if used, _ := store.GetQuotaBytes(ctx, testProjX); used != 0 {
		t.Errorf("an expired upload must not be charged, got %d", used)
	}

	// Just inside the window the confirm still works, so the two windows do
	// not overlap and no object is ever in both.
	blobs.putAt(testProjX, bucket.ID, "fresh.bin", 10, "text/plain", now.Add(-confirmWindow(DefaultUnconfirmedGrace)+time.Second))
	if _, err := svc.ConfirmUpload(ctx, testProjX, "files", "FREE", "u", ConfirmUploadRequest{Key: "fresh.bin"}); err != nil {
		t.Fatalf("an upload inside the confirm window must still be accepted: %v", err)
	}
}

// The reaper only takes objects that are past the grace, which is strictly
// later than the last moment a confirm would have been accepted.
func TestService_ReapAndConfirmWindowsDoNotOverlap(t *testing.T) {
	svc, _, blobs, bucket := confirmableService(t, nil)
	ctx := context.Background()
	now := time.Now().UTC()
	svc.now = func() time.Time { return now }

	// Written after the confirm window closed but before the reaper's grace.
	written := now.Add(-DefaultUnconfirmedGrace + time.Minute)
	blobs.putAt(testProjX, bucket.ID, "limbo.bin", 10, "text/plain", written)

	report, err := svc.ReapUnconfirmedUploads(ctx, DefaultUnconfirmedGrace, now)
	if err != nil {
		t.Fatalf("ReapUnconfirmedUploads: %v", err)
	}
	if len(report.Deleted) != 0 {
		t.Errorf("the reaper must not take an object the grace still covers: %v", report.Deleted)
	}
	if _, err := svc.ConfirmUpload(ctx, testProjX, "files", "FREE", "u", ConfirmUploadRequest{Key: "limbo.bin"}); err == nil {
		t.Error("an object past the confirm window must not be confirmable")
	}
}

// A bucket being deleted is the delete path's to empty; the reaper leaves it
// alone rather than racing it over the same keys.
func TestService_Reaper_SkipsBucketsBeingDeleted(t *testing.T) {
	svc, store, blobs, bucket := confirmableService(t, nil)
	ctx := context.Background()
	now := time.Now().UTC()
	blobs.putAt(testProjX, bucket.ID, "old.bin", 10, "text/plain", now.Add(-3*time.Hour))
	if err := store.SetBucketStatus(ctx, testProjX, "files", BucketStatusDeleting); err != nil {
		t.Fatalf("SetBucketStatus: %v", err)
	}

	report, err := svc.ReapUnconfirmedUploads(ctx, time.Hour, now)
	if err != nil {
		t.Fatalf("ReapUnconfirmedUploads: %v", err)
	}
	if len(report.Deleted) != 0 {
		t.Errorf("a bucket mid-delete is not the reaper's to empty: %v", report.Deleted)
	}
}

// Finding 8 — everything under a bucket's prefix belongs to that bucket and
// nothing else, because ids are never reused. The delete purges uncatalogued
// bytes itself instead of refusing until something else clears them.
func TestService_DeleteBucket_PurgesUncataloguedObjects(t *testing.T) {
	svc, store, blobs, bucket := confirmableService(t, nil)
	ctx := context.Background()
	blobs.put(testProjX, bucket.ID, "recorded.txt", 10, "text/plain")
	if _, err := svc.ConfirmUpload(ctx, testProjX, "files", "FREE", "u", ConfirmUploadRequest{Key: "recorded.txt"}); err != nil {
		t.Fatalf("ConfirmUpload: %v", err)
	}
	blobs.put(testProjX, bucket.ID, "never-confirmed.bin", 999, "application/octet-stream")

	if err := svc.DeleteBucket(ctx, testProjX, "files"); err != nil {
		t.Fatalf("DeleteBucket must clear its own prefix, not refuse: %v", err)
	}
	if blobs.has(testProjX, bucket.ID, "never-confirmed.bin") {
		t.Error("uncatalogued bytes survived the bucket delete")
	}
	if b, _ := store.GetBucket(ctx, testProjX, "files"); b != nil {
		t.Error("bucket record should be gone")
	}
}

// Finding 9 — a public bucket serves its objects to anyone, so a type the
// browser will render as a document is refused unless the bucket's owner
// named it explicitly.
func TestService_PublicBucket_RefusesRenderableTypes(t *testing.T) {
	store := newMemStore()
	blobs := newFakeObjectStore()
	svc := NewServiceWithObjectStore(store, blobs, nil)
	ctx := context.Background()
	if _, err := svc.CreateBucket(ctx, testProjX, CreateBucketRequest{Name: "public", Public: true}); err != nil {
		t.Fatalf("CreateBucket: %v", err)
	}

	for _, mimeType := range []string{
		"text/html", "application/xhtml+xml", "image/svg+xml",
		"text/xml", "application/xml", "text/javascript", "application/javascript",
	} {
		_, err := svc.SignUploadURL(ctx, testProjX, "public", "FREE", UploadURLRequest{
			Key: "x", MimeType: mimeType, Size: 10,
		})
		if err == nil {
			t.Errorf("%q must not be uploadable to a public bucket by default", mimeType)
		}
	}
	// An image is unremarkable.
	if _, err := svc.SignUploadURL(ctx, testProjX, "public", "FREE", UploadURLRequest{
		Key: "logo.png", MimeType: "image/png", Size: 10,
	}); err != nil {
		t.Errorf("an image must still be uploadable: %v", err)
	}
}

// A bucket whose owner listed the type explicitly is taken at its word.
func TestService_PublicBucket_AllowsRenderableTypesWhenNamed(t *testing.T) {
	store := newMemStore()
	blobs := newFakeObjectStore()
	svc := NewServiceWithObjectStore(store, blobs, nil)
	ctx := context.Background()
	_, _ = svc.CreateBucket(ctx, testProjX, CreateBucketRequest{
		Name: "site", Public: true, AllowedMimeTypes: []string{"text/html"},
	})

	if _, err := svc.SignUploadURL(ctx, testProjX, "site", "FREE", UploadURLRequest{
		Key: "index.html", MimeType: "text/html", Size: 10,
	}); err != nil {
		t.Errorf("an explicitly allowed type must be accepted: %v", err)
	}
}

// A private bucket may hold anything; the signed GET is what neutralises it.
func TestService_PrivateBucket_AllowsRenderableTypes(t *testing.T) {
	store := newMemStore()
	blobs := newFakeObjectStore()
	svc := NewServiceWithObjectStore(store, blobs, nil)
	ctx := context.Background()
	_, _ = svc.CreateBucket(ctx, testProjX, CreateBucketRequest{Name: "private"})

	if _, err := svc.SignUploadURL(ctx, testProjX, "private", "FREE", UploadURLRequest{
		Key: "page.html", MimeType: "text/html", Size: 10,
	}); err != nil {
		t.Errorf("a private bucket may hold markup: %v", err)
	}
}

// The signed GET is where a private object of a renderable type is made
// inert: the signature pins how the response must be served.
func TestService_SignDownloadURL_NeutralisesRenderableTypes(t *testing.T) {
	svc, _, blobs, bucket := confirmableService(t, nil)
	ctx := context.Background()
	blobs.put(testProjX, bucket.ID, "page.html", 10, "text/html")
	if _, err := svc.ConfirmUpload(ctx, testProjX, "files", "FREE", "u", ConfirmUploadRequest{Key: "page.html"}); err != nil {
		t.Fatalf("ConfirmUpload: %v", err)
	}
	blobs.put(testProjX, bucket.ID, "logo.png", 10, "image/png")
	if _, err := svc.ConfirmUpload(ctx, testProjX, "files", "FREE", "u", ConfirmUploadRequest{Key: "logo.png"}); err != nil {
		t.Fatalf("ConfirmUpload: %v", err)
	}

	markup, err := svc.SignDownloadURL(ctx, testProjX, "files", "page.html")
	if err != nil {
		t.Fatalf("SignDownloadURL: %v", err)
	}
	if !strings.Contains(markup.URL, "response-content-disposition") {
		t.Errorf("markup must be served as a download: %s", markup.URL)
	}
	image, err := svc.SignDownloadURL(ctx, testProjX, "files", "logo.png")
	if err != nil {
		t.Fatalf("SignDownloadURL: %v", err)
	}
	if strings.Contains(image.URL, "response-content-disposition") {
		t.Errorf("an image should still be served inline: %s", image.URL)
	}
}

// The reaper reports what it could not do rather than stopping: a catalogue
// read that fails, or bytes it cannot delete, mark that bucket failed.
func TestService_Reaper_ReportsPerObjectFailures(t *testing.T) {
	store := newErrStore()
	blobs := newFakeObjectStore()
	svc := NewServiceWithObjectStore(store, blobs, nil)
	ctx := context.Background()
	bucket, _ := svc.CreateBucket(ctx, testProjX, CreateBucketRequest{Name: "files"})
	now := time.Now().UTC()
	blobs.putAt(testProjX, bucket.ID, "old.bin", 10, "text/plain", now.Add(-3*time.Hour))

	store.getObjectErr = errors.New("platform db unavailable")
	report, err := svc.ReapUnconfirmedUploads(ctx, time.Hour, now)
	if err != nil {
		t.Fatalf("ReapUnconfirmedUploads: %v", err)
	}
	if len(report.Failed) != 1 || len(report.Deleted) != 0 {
		t.Fatalf("a failed row lookup must be reported, not swallowed: %+v", report)
	}

	store.getObjectErr = nil
	blobs.deleteErr = errors.New("object store refuses deletes")
	report, err = svc.ReapUnconfirmedUploads(ctx, time.Hour, now)
	if err != nil {
		t.Fatalf("ReapUnconfirmedUploads: %v", err)
	}
	if len(report.Failed) != 1 || len(report.Deleted) != 0 {
		t.Fatalf("a failed delete must be reported: %+v", report)
	}
}

// A cancelled sweep stops where it is rather than running to the end.
func TestService_Reaper_StopsOnCancelledContext(t *testing.T) {
	svc, _, _, _ := confirmableService(t, nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := svc.ReapUnconfirmedUploads(ctx, time.Hour, time.Now()); err == nil {
		t.Fatal("a cancelled sweep must report that it did not finish")
	}
}

// A grace shorter than the safety margin still leaves a window in which a
// confirmation is accepted, rather than refusing every upload.
func TestConfirmWindow_ShortGraceStillLeavesRoom(t *testing.T) {
	if got := confirmWindow(time.Minute); got <= 0 || got >= time.Minute {
		t.Errorf("confirmWindow(1m) = %s, want a positive window shorter than the grace", got)
	}
	if got := confirmWindow(time.Hour); got != time.Hour-confirmSafetyMargin {
		t.Errorf("confirmWindow(1h) = %s, want %s", got, time.Hour-confirmSafetyMargin)
	}
}

// A bucket delete that cannot clear its stray bytes fails rather than
// dropping the record that points at the prefix holding them.
func TestService_DeleteBucket_ReportsStrayPurgeFailure(t *testing.T) {
	svc, store, blobs, bucket := confirmableService(t, nil)
	ctx := context.Background()
	blobs.put(testProjX, bucket.ID, "stray.bin", 10, "application/octet-stream")
	blobs.deleteErr = errors.New("object store refuses deletes")

	err := svc.DeleteBucket(ctx, testProjX, "files")
	if err == nil || !strings.Contains(err.Error(), "stray object") {
		t.Fatalf("want a stray-purge failure, got %v", err)
	}
	if b, _ := store.GetBucket(ctx, testProjX, "files"); b == nil {
		t.Error("the bucket record must survive so the bytes stay findable")
	}
}

// An object whose recorded type cannot be parsed is served as a download:
// a type nothing can vouch for is the last one to render inline.
func TestService_SignDownloadURL_UnusableRecordedTypeIsDownloaded(t *testing.T) {
	svc, store, blobs, bucket := confirmableService(t, nil)
	ctx := context.Background()
	blobs.put(testProjX, bucket.ID, "odd.bin", 10, "text/plain")
	if _, err := svc.ConfirmUpload(ctx, testProjX, "files", "FREE", "u", ConfirmUploadRequest{Key: "odd.bin"}); err != nil {
		t.Fatalf("ConfirmUpload: %v", err)
	}
	// Rewrite the recorded type to something no parser accepts.
	obj, _ := store.GetObject(ctx, bucket.ID, "odd.bin")
	obj.MimeType = "not a media type"

	out, err := svc.SignDownloadURL(ctx, testProjX, "files", "odd.bin")
	if err != nil {
		t.Fatalf("SignDownloadURL: %v", err)
	}
	if !strings.Contains(out.URL, "response-content-disposition") {
		t.Errorf("an unusable type must be served as a download: %s", out.URL)
	}
}
