package storagesvc

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// EXC-405 follow-up, finding 2 — the catalogue used to hold the key the
// caller sent while the object store held a cleaned version of it. "a//b" was
// stored as "a/b" and catalogued as "a//b", so a listing of the store found a
// key no row named: to the reaper that looked exactly like an abandoned
// upload, and after the grace it deleted a confirmed, charged object.
//
// One function decides what a key is, every entry point uses it, and a key
// that is not already canonical is refused rather than quietly rewritten —
// so the catalogue and the store always hold the identical string.

func TestValidateObjectKey_RejectsNonCanonicalAndUnsafeKeys(t *testing.T) {
	bad := map[string]string{
		"empty":            "",
		"double slash":     "a//b",
		"dot segment":      "a/./b",
		"parent segment":   "a/../b",
		"leading slash":    "/a",
		"trailing slash":   "a/",
		"bare dot":         ".",
		"bare parent":      "..",
		"backslash":        `a\b`,
		"null byte":        "a\x00b",
		"control char":     "a\x01b",
		"newline":          "a\nb",
		"delete char":      "a\x7fb",
		"staging segment":  ".staging/upl_1",
		"staging deeper":   ".staging/a/b",
		"over-long":        strings.Repeat("a", maxObjectKeyLen+1),
		"only slashes":     "//",
		"trailing dot dir": "a/b/..",
	}
	for name, key := range bad {
		if err := validateObjectKey(key); err == nil {
			t.Errorf("%s (%q) must be refused", name, key)
		} else {
			var invalid *ValidationError
			if !errors.As(err, &invalid) {
				t.Errorf("%s: a bad key is the caller's to fix, got %T", name, err)
			}
		}
	}

	good := []string{
		"a.txt",
		"docs/a.txt",
		"deeply/nested/path/file name.png",
		"a-b_c.d",
		"staging/not-reserved.txt",
		"unicode/ünïcødé.txt",
	}
	for _, key := range good {
		if err := validateObjectKey(key); err != nil {
			t.Errorf("%q should be accepted: %v", key, err)
		}
	}
}

// Every entry point uses it, so a key the store would have cleaned never
// reaches either plane.
func TestService_EveryKeyEntryPointRejectsANonCanonicalKey(t *testing.T) {
	svc, _, _, bucket := stagedService(t, nil)
	ctx := context.Background()
	const bad = "a//b"

	calls := map[string]func() error{
		"SignUploadURL": func() error {
			_, err := svc.SignUploadURL(ctx, testProjX, "files", "FREE", UploadURLRequest{Key: bad, MimeType: "text/plain", Size: 1})
			return err
		},
		"ConfirmUpload": func() error {
			_, err := svc.ConfirmUpload(ctx, testProjX, "files", "FREE", "u", ConfirmUploadRequest{Key: bad, UploadID: "upl_x"})
			return err
		},
		"DeleteObject": func() error { return svc.DeleteObject(ctx, testProjX, "files", bad) },
		"SignDownloadURL": func() error {
			_, err := svc.SignDownloadURL(ctx, testProjX, "files", bad)
			return err
		},
		"StartResumableUpload": func() error {
			_, _, err := svc.StartResumableUpload(ctx, testProjX, "files", "FREE", UploadURLRequest{Key: bad, MimeType: "text/plain", Size: 1})
			return err
		},
		"ListObjects": func() error {
			_, err := svc.ListObjects(ctx, testProjX, "files", ListObjectsRequest{Prefix: bad})
			return err
		},
	}
	for name, call := range calls {
		err := call()
		var invalid *ValidationError
		if err == nil || !errors.As(err, &invalid) {
			t.Errorf("%s: a non-canonical key must be refused, got %v", name, err)
		}
	}
	_ = bucket
}

// The staging namespace is the platform's, not the caller's: no route can
// name it.
func TestService_KeyEntryPointsRefuseTheStagingNamespace(t *testing.T) {
	svc, _, _, _ := stagedService(t, nil)
	ctx := context.Background()
	staged := stagingObjectKey("upl_someone_elses")

	if _, err := svc.SignDownloadURL(ctx, testProjX, "files", staged); err == nil {
		t.Error("a download URL must not be mintable for a staged object")
	}
	if err := svc.DeleteObject(ctx, testProjX, "files", staged); err == nil {
		t.Error("a staged object must not be deletable through the object route")
	}
	if _, err := svc.ListObjects(ctx, testProjX, "files", ListObjectsRequest{Prefix: stagingPrefix}); err == nil {
		t.Error("the staging namespace must not be listable")
	}
}

// What the catalogue holds and what the store holds is the same string, so a
// listing of one can be matched against the other.
func TestService_CatalogueAndStoreHoldTheSameKey(t *testing.T) {
	svc, store, blobs, bucket := stagedService(t, nil)
	ctx := context.Background()
	const key = "docs/report v2.txt"
	if _, err := confirmStaged(t, svc, blobs, bucket, key, "text/plain", 10); err != nil {
		t.Fatalf("confirm: %v", err)
	}
	row, _ := store.GetObject(ctx, bucket.ID, key)
	if row == nil {
		t.Fatalf("no row catalogued under %q", key)
	}
	if !blobs.has(testProjX, bucket.ID, key) {
		t.Fatalf("the store holds a different key than the catalogue for %q", key)
	}
}

// The reaper looks in one place only. An object on a live key with no row is
// not its business — a bucket or project purge deals with those — and it has
// no way to name one.
func TestService_Reaper_NeverTouchesALiveKey(t *testing.T) {
	svc, _, blobs, bucket := stagedService(t, nil)
	ctx := context.Background()
	now := time.Now().UTC()
	blobs.putAt(testProjX, bucket.ID, "orphan.bin", 10, "text/plain", now.Add(-3*time.Hour))
	blobs.putAt(testProjX, bucket.ID, stagingObjectKey("upl_old"), 10, "text/plain", now.Add(-3*time.Hour))

	report, err := svc.ReapUnconfirmedUploads(ctx, time.Hour, now)
	if err != nil {
		t.Fatalf("ReapUnconfirmedUploads: %v", err)
	}
	if !blobs.has(testProjX, bucket.ID, "orphan.bin") {
		t.Fatal("the reaper deleted an object on a live key")
	}
	if blobs.has(testProjX, bucket.ID, stagingObjectKey("upl_old")) {
		t.Error("the reaper should have collected the abandoned staged upload")
	}
	if len(report.Deleted) != 1 {
		t.Errorf("report: %v", report.Deleted)
	}
}
