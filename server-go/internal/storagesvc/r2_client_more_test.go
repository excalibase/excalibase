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

func TestR2_PublicURL_BadKeyReturnsEmpty(t *testing.T) {
	c := newR2(t)
	if got := c.PublicURL(testProjABC, "files", "../escape"); got != "" {
		t.Errorf("bad key should give empty URL, got %q", got)
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
