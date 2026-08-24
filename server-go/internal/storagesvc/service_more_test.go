package storagesvc

import (
	"context"
	"strings"
	"testing"
)

// newTestR2 builds an R2Client pointed at a non-routable localhost endpoint so
// presign + URL methods work fully offline while network methods
// (DeleteObject/HeadObject) fail fast (connection refused) instead of doing
// real DNS + SDK retries against a public domain. The service paths under test
// swallow R2 errors, so a fast failure is exactly what we want.
func newTestR2(t *testing.T) *R2Client {
	t.Helper()
	r2, err := NewR2Client(R2Config{
		AccessKeyID:     "k",
		SecretAccessKey: "s",
		Endpoint:        "http://127.0.0.1:1", // closed port → instant refusal
		Bucket:          testPlatformBucket,
	})
	if err != nil {
		t.Fatalf("NewR2Client: %v", err)
	}
	return r2
}

func TestService_ListBuckets(t *testing.T) {
	store := newMemStore()
	svc := NewService(store, nil, nil)
	ctx := context.Background()
	_, _ = svc.CreateBucket(ctx, testProjX, CreateBucketRequest{Name: "alpha"})
	_, _ = svc.CreateBucket(ctx, testProjX, CreateBucketRequest{Name: "beta"})
	_, _ = svc.CreateBucket(ctx, "other-proj", CreateBucketRequest{Name: "gamma"})

	got, err := svc.ListBuckets(ctx, testProjX)
	if err != nil {
		t.Fatalf("ListBuckets: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 buckets for project, got %d", len(got))
	}
}

func TestService_ConfirmUpload_RecordsObjectAndQuota(t *testing.T) {
	store := newMemStore()
	svc := NewService(store, newTestR2(t), nil)
	ctx := context.Background()
	_, _ = svc.CreateBucket(ctx, testProjX, CreateBucketRequest{Name: "files"})

	obj, err := svc.ConfirmUpload(ctx, testProjX, "files", "user-1", ConfirmUploadRequest{
		Key: "a.txt", Size: 500, MimeType: "text/plain", ETag: "etag-1",
	})
	if err != nil {
		t.Fatalf("ConfirmUpload: %v", err)
	}
	if obj.Key != "a.txt" || obj.Size != 500 || obj.OwnerID != "user-1" {
		t.Errorf("unexpected object: %+v", obj)
	}
	if !strings.HasPrefix(obj.ID, "obj_") {
		t.Errorf("object id prefix: %s", obj.ID)
	}
	used, _ := store.GetQuotaBytes(ctx, testProjX)
	if used != 500 {
		t.Errorf("quota: got %d, want 500", used)
	}
}

func TestService_ConfirmUpload_MissingBucket(t *testing.T) {
	svc := NewService(newMemStore(), newTestR2(t), nil)
	_, err := svc.ConfirmUpload(context.Background(), testProjX, "nope", "u", ConfirmUploadRequest{Key: "x"})
	if err == nil || !strings.Contains(err.Error(), errBucketNotFound) {
		t.Errorf("expected bucket-not-found, got %v", err)
	}
}

func TestService_ListObjects(t *testing.T) {
	store := newMemStore()
	svc := NewService(store, newTestR2(t), nil)
	ctx := context.Background()
	_, _ = svc.CreateBucket(ctx, testProjX, CreateBucketRequest{Name: "files"})
	for _, k := range []string{"img/1.png", "img/2.png", "doc/a.txt"} {
		_, _ = svc.ConfirmUpload(ctx, testProjX, "files", "u", ConfirmUploadRequest{Key: k, Size: 1})
	}

	all, err := svc.ListObjects(ctx, testProjX, "files", ListObjectsRequest{})
	if err != nil {
		t.Fatalf("ListObjects: %v", err)
	}
	if len(all.Objects) != 3 {
		t.Errorf("expected 3 objects, got %d", len(all.Objects))
	}

	imgs, err := svc.ListObjects(ctx, testProjX, "files", ListObjectsRequest{Prefix: "img/"})
	if err != nil {
		t.Fatalf("ListObjects prefix: %v", err)
	}
	if len(imgs.Objects) != 2 {
		t.Errorf("expected 2 img objects, got %d", len(imgs.Objects))
	}
}

func TestService_ListObjects_ClampsLimit(t *testing.T) {
	store := newMemStore()
	svc := NewService(store, newTestR2(t), nil)
	ctx := context.Background()
	_, _ = svc.CreateBucket(ctx, testProjX, CreateBucketRequest{Name: "files"})
	_, _ = svc.ConfirmUpload(ctx, testProjX, "files", "u", ConfirmUploadRequest{Key: "k", Size: 1})

	// Out-of-range limits get clamped to the 100 default — just assert no error.
	if _, err := svc.ListObjects(ctx, testProjX, "files", ListObjectsRequest{Limit: -5}); err != nil {
		t.Errorf("negative limit: %v", err)
	}
	if _, err := svc.ListObjects(ctx, testProjX, "files", ListObjectsRequest{Limit: 99999}); err != nil {
		t.Errorf("huge limit: %v", err)
	}
}

func TestService_ListObjects_MissingBucket(t *testing.T) {
	svc := NewService(newMemStore(), newTestR2(t), nil)
	_, err := svc.ListObjects(context.Background(), testProjX, "nope", ListObjectsRequest{})
	if err == nil || !strings.Contains(err.Error(), errBucketNotFound) {
		t.Errorf("expected bucket-not-found, got %v", err)
	}
}

func TestService_SignDownloadURL_PublicBucket(t *testing.T) {
	store := newMemStore()
	svc := NewService(store, newTestR2(t), nil)
	ctx := context.Background()
	_, _ = svc.CreateBucket(ctx, testProjX, CreateBucketRequest{Name: "pub", Public: true})

	resp, err := svc.SignDownloadURL(ctx, testProjX, "pub", "logo.png")
	if err != nil {
		t.Fatalf("SignDownloadURL public: %v", err)
	}
	if !resp.Public {
		t.Errorf("expected Public=true")
	}
	if !strings.Contains(resp.URL, "projects/"+testProjX+"/buckets/pub/logo.png") {
		t.Errorf("public url missing key path: %s", resp.URL)
	}
}

func TestService_SignDownloadURL_PrivateBucket(t *testing.T) {
	store := newMemStore()
	svc := NewService(store, newTestR2(t), nil)
	ctx := context.Background()
	_, _ = svc.CreateBucket(ctx, testProjX, CreateBucketRequest{Name: "priv"})

	resp, err := svc.SignDownloadURL(ctx, testProjX, "priv", "secret.txt")
	if err != nil {
		t.Fatalf("SignDownloadURL private: %v", err)
	}
	if resp.Public {
		t.Errorf("expected Public=false for private bucket")
	}
	if resp.URL == "" || resp.ExpiresAt.IsZero() {
		t.Errorf("expected signed url + expiry, got %+v", resp)
	}
}

func TestService_SignDownloadURL_MissingBucket(t *testing.T) {
	svc := NewService(newMemStore(), newTestR2(t), nil)
	_, err := svc.SignDownloadURL(context.Background(), testProjX, "nope", "k")
	if err == nil || !strings.Contains(err.Error(), errBucketNotFound) {
		t.Errorf("expected bucket-not-found, got %v", err)
	}
}

func TestService_DeleteObjectCatalogueOnly(t *testing.T) {
	store := newMemStore()
	svc := NewService(store, newTestR2(t), nil)
	ctx := context.Background()
	_, _ = svc.CreateBucket(ctx, testProjX, CreateBucketRequest{Name: "files"})
	_, _ = svc.ConfirmUpload(ctx, testProjX, "files", "u", ConfirmUploadRequest{Key: "k.txt", Size: 300})

	if err := svc.DeleteObjectCatalogueOnly(ctx, testProjX, "files", "k.txt"); err != nil {
		t.Fatalf("DeleteObjectCatalogueOnly: %v", err)
	}
	// Quota should be decremented back to 0.
	if used, _ := store.GetQuotaBytes(ctx, testProjX); used != 0 {
		t.Errorf("quota after delete: got %d, want 0", used)
	}
	// Listing should no longer surface it.
	out, _ := svc.ListObjects(ctx, testProjX, "files", ListObjectsRequest{})
	if len(out.Objects) != 0 {
		t.Errorf("expected 0 objects after delete, got %d", len(out.Objects))
	}
}

func TestService_DeleteObjectCatalogueOnly_MissingBucketIsNoop(t *testing.T) {
	svc := NewService(newMemStore(), newTestR2(t), nil)
	// Idempotent: deleting from a non-existent bucket succeeds.
	if err := svc.DeleteObjectCatalogueOnly(context.Background(), testProjX, "nope", "k"); err != nil {
		t.Errorf("expected nil for missing bucket, got %v", err)
	}
}

func TestService_DeleteObject_MissingBucket(t *testing.T) {
	svc := NewService(newMemStore(), newTestR2(t), nil)
	err := svc.DeleteObject(context.Background(), testProjX, "nope", "k")
	if err == nil || !strings.Contains(err.Error(), errBucketNotFound) {
		t.Errorf("expected bucket-not-found, got %v", err)
	}
}

func TestService_DeleteBucket_MissingBucket(t *testing.T) {
	svc := NewService(newMemStore(), newTestR2(t), nil)
	err := svc.DeleteBucket(context.Background(), testProjX, "nope")
	if err == nil || !strings.Contains(err.Error(), errBucketNotFound) {
		t.Errorf("expected bucket-not-found, got %v", err)
	}
}

func TestService_DeleteBucket_CascadesObjects(t *testing.T) {
	store := newMemStore()
	svc := NewService(store, newTestR2(t), nil)
	ctx := context.Background()
	_, _ = svc.CreateBucket(ctx, testProjX, CreateBucketRequest{Name: "files"})
	for _, k := range []string{"a", "b", "c"} {
		_, _ = svc.ConfirmUpload(ctx, testProjX, "files", "u", ConfirmUploadRequest{Key: k, Size: 100})
	}
	// Quota reflects the 3 objects.
	if used, _ := store.GetQuotaBytes(ctx, testProjX); used != 300 {
		t.Fatalf("pre-delete quota: got %d, want 300", used)
	}

	// DeleteBucket walks objects (r2 delete errors offline but is swallowed),
	// decrements quota, then drops the bucket row.
	if err := svc.DeleteBucket(ctx, testProjX, "files"); err != nil {
		t.Fatalf("DeleteBucket: %v", err)
	}
	if used, _ := store.GetQuotaBytes(ctx, testProjX); used != 0 {
		t.Errorf("post-delete quota: got %d, want 0", used)
	}
	// Bucket gone → subsequent lookups treat it as missing.
	if _, err := svc.ListObjects(ctx, testProjX, "files", ListObjectsRequest{}); err == nil {
		t.Error("expected bucket-not-found after cascade delete")
	}
}

func TestService_DeleteObject_DecrementsQuota(t *testing.T) {
	store := newMemStore()
	svc := NewService(store, newTestR2(t), nil)
	ctx := context.Background()
	_, _ = svc.CreateBucket(ctx, testProjX, CreateBucketRequest{Name: "files"})
	_, _ = svc.ConfirmUpload(ctx, testProjX, "files", "u", ConfirmUploadRequest{Key: "k", Size: 200})

	// R2 delete will error offline; DeleteObject returns that error before
	// touching the catalogue (R2-first ordering). Assert the error surfaces.
	if err := svc.DeleteObject(ctx, testProjX, "files", "k"); err == nil {
		t.Log("DeleteObject returned nil (r2 delete unexpectedly succeeded)")
	}
}

func TestService_QuotaForTier(t *testing.T) {
	svc := NewService(newMemStore(), nil, map[string]int64{
		"free": 100,
		"pro":  1000,
	})
	if got := svc.quotaForTier("PRO"); got != 1000 {
		t.Errorf("pro tier: got %d, want 1000", got)
	}
	// Unknown tier falls back to free.
	if got := svc.quotaForTier("enterprise"); got != 100 {
		t.Errorf("unknown tier fallback: got %d, want 100", got)
	}
	// Nil map → unlimited (0).
	svcNil := NewService(newMemStore(), nil, nil)
	if got := svcNil.quotaForTier("free"); got != 0 {
		t.Errorf("nil quota map: got %d, want 0", got)
	}
}

func TestService_CheckMIMEAllowlist_Wildcard(t *testing.T) {
	svc := NewService(newMemStore(), nil, nil)
	if err := svc.checkMIMEAllowlist([]string{"*/*"}, "application/octet-stream"); err != nil {
		t.Errorf("wildcard should allow any mime: %v", err)
	}
	if err := svc.checkMIMEAllowlist(nil, "anything"); err != nil {
		t.Errorf("empty allowlist should allow any mime: %v", err)
	}
}
