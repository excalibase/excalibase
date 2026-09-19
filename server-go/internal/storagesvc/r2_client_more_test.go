package storagesvc

import (
	"context"
	"strings"
	"testing"
	"time"
)

func newR2(t *testing.T) *R2Client {
	t.Helper()
	c, err := NewR2Client(R2Config{
		AccessKeyID: "k", SecretAccessKey: "s",
		Endpoint: "https://acct.r2.cloudflarestorage.com",
		Bucket:   testPlatformBucket,
	})
	if err != nil {
		t.Fatalf("NewR2Client: %v", err)
	}
	return c
}

func TestR2_SignedPutURL(t *testing.T) {
	c := newR2(t)
	url, expires, err := c.SignedPutURL(context.Background(), testProjABC, "files", "a.txt", "text/plain", 0)
	if err != nil {
		t.Fatalf("SignedPutURL: %v", err)
	}
	if !strings.Contains(url, "projects/proj-abc/buckets/files/a.txt") {
		t.Errorf("signed put url missing key path: %s", url)
	}
	if !expires.After(time.Now()) {
		t.Errorf("expiry not in the future: %v", expires)
	}
}

func TestR2_SignedPutURL_RejectsBadKey(t *testing.T) {
	c := newR2(t)
	if _, _, err := c.SignedPutURL(context.Background(), testProjABC, "files", "../escape", "", 0); err == nil {
		t.Error("traversal key should fail")
	}
}

func TestR2_SignedGetURL(t *testing.T) {
	c := newR2(t)
	url, expires, err := c.SignedGetURL(context.Background(), testProjABC, "files", "b.txt", time.Minute)
	if err != nil {
		t.Fatalf("SignedGetURL: %v", err)
	}
	if !strings.Contains(url, "projects/proj-abc/buckets/files/b.txt") {
		t.Errorf("signed get url missing key path: %s", url)
	}
	if !expires.After(time.Now()) {
		t.Errorf("expiry not in the future: %v", expires)
	}
}

func TestR2_SignedGetURL_RejectsBadKey(t *testing.T) {
	c := newR2(t)
	if _, _, err := c.SignedGetURL(context.Background(), testProjABC, "files", "..", 0); err == nil {
		t.Error("bad key should fail")
	}
}

func TestR2_PublicURL_BadKeyReportsError(t *testing.T) {
	c := newR2(t)
	got, err := c.PublicURL(testProjABC, "files", "../escape")
	if err == nil {
		t.Errorf("bad key should report an error, got URL %q", got)
	}
}

func TestR2_DeleteObject_BadKey(t *testing.T) {
	c := newR2(t)
	// Traversal key is rejected before any network call.
	if err := c.DeleteObject(context.Background(), testProjABC, "files", "../x"); err == nil {
		t.Error("bad key delete should fail")
	}
}

func TestR2_HeadObject_BadKey(t *testing.T) {
	c := newR2(t)
	if _, _, _, err := c.HeadObject(context.Background(), testProjABC, "files", ".."); err == nil {
		t.Error("bad key head should fail")
	}
}

// TestBucketPrefix_TrailingSlashIsolatesBuckets — the emptiness check that
// gates a bucket delete lists by this prefix. Without the trailing slash,
// "assets" would match every key of "assets2": one bucket's delete could be
// blocked by, or could delete, a neighbour's objects.
func TestBucketPrefix_TrailingSlashIsolatesBuckets(t *testing.T) {
	assets, err := bucketPrefix(testProjABC, "assets")
	if err != nil {
		t.Fatalf("bucketPrefix: %v", err)
	}
	if want := "projects/proj-abc/buckets/assets/"; assets != want {
		t.Fatalf("bucketPrefix: got %q, want %q", assets, want)
	}
	neighbour, err := objectKey(testProjABC, "assets2", "keep.bin")
	if err != nil {
		t.Fatalf("objectKey: %v", err)
	}
	if strings.HasPrefix(neighbour, assets) {
		t.Errorf("%q must not fall under the %q prefix", neighbour, assets)
	}
	own, _ := objectKey(testProjABC, "assets", "keep.bin")
	if !strings.HasPrefix(own, assets) {
		t.Errorf("%q must fall under the %q prefix", own, assets)
	}
}

func TestBucketPrefix_RequiresProjectAndBucket(t *testing.T) {
	if _, err := bucketPrefix("", "b"); err == nil {
		t.Error("empty project should fail")
	}
	if _, err := bucketPrefix("p", ""); err == nil {
		t.Error("empty bucket should fail")
	}
}

func TestR2_ListObjectKeys_RejectsBadBucket(t *testing.T) {
	c := newR2(t)
	if _, err := c.ListObjectKeys(context.Background(), testProjABC, "", 10); err == nil {
		t.Error("missing bucket should fail before any network call")
	}
}
