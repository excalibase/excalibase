package storagesvc

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/aws/smithy-go"
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
	url, expires, err := c.SignedPutURL(context.Background(), testProjABC, "files", "a.txt", "text/plain", 10, 0)
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
	if _, _, err := c.SignedPutURL(context.Background(), testProjABC, "files", "../escape", "text/plain", 10, 0); err == nil {
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
// id "bkt_1" would match every key of "bkt_12": one bucket's delete could be
// blocked by, or could delete, a neighbour's objects.
func TestBucketPrefix_TrailingSlashIsolatesBuckets(t *testing.T) {
	own, err := bucketPrefix(testProjABC, "bkt_1")
	if err != nil {
		t.Fatalf("bucketPrefix: %v", err)
	}
	if want := "projects/proj-abc/buckets/bkt_1/"; own != want {
		t.Fatalf("bucketPrefix: got %q, want %q", own, want)
	}
	neighbour, err := objectKey(testProjABC, "bkt_12", "keep.bin")
	if err != nil {
		t.Fatalf("objectKey: %v", err)
	}
	if strings.HasPrefix(neighbour, own) {
		t.Errorf("%q must not fall under the %q prefix", neighbour, own)
	}
	mine, _ := objectKey(testProjABC, "bkt_1", "keep.bin")
	if !strings.HasPrefix(mine, own) {
		t.Errorf("%q must fall under the %q prefix", mine, own)
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

func TestR2_ListObjects_RejectsBadBucket(t *testing.T) {
	c := newR2(t)
	if _, err := c.ListObjects(context.Background(), testProjABC, "", 10); err == nil {
		t.Error("missing bucket should fail before any network call")
	}
}

// A signed PUT is only meaningful if it binds what may be uploaded, so the
// client refuses to sign one without both a type and a length.
func TestR2_SignedPutURL_RequiresTypeAndLength(t *testing.T) {
	c := newR2(t)
	if _, _, err := c.SignedPutURL(context.Background(), testProjABC, "files", "a.txt", "", 10, 0); err == nil {
		t.Error("signing without a content type should fail")
	}
	if _, _, err := c.SignedPutURL(context.Background(), testProjABC, "files", "a.txt", "text/plain", 0, 0); err == nil {
		t.Error("signing without a content length should fail")
	}
}

func TestR2_ListObjects_ReportsBackendError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`<Error><Code>AccessDenied</Code></Error>`))
	}))
	defer srv.Close()
	c, err := NewR2Client(R2Config{
		AccessKeyID: "k", SecretAccessKey: "s",
		Endpoint: srv.URL, Bucket: testPlatformBucket,
	})
	if err != nil {
		t.Fatalf("NewR2Client: %v", err)
	}
	if _, err := c.ListObjects(context.Background(), testProjABC, "files", 0); err == nil {
		t.Error("a refused listing must not read as an empty bucket")
	}
}

// isNotFound only says yes for the object store's own "it isn't there"
// codes; a transport error or any other API error is a real failure.
func TestIsNotFound(t *testing.T) {
	if isNotFound(errors.New("connection refused")) {
		t.Error("a transport error is not a missing key")
	}
	if isNotFound(&smithy.GenericAPIError{Code: "AccessDenied"}) {
		t.Error("AccessDenied is not a missing key")
	}
	if !isNotFound(&smithy.GenericAPIError{Code: "NoSuchKey"}) {
		t.Error("NoSuchKey is a missing key")
	}
	if !isNotFound(&smithy.GenericAPIError{Code: "NotFound"}) {
		t.Error("NotFound is a missing key")
	}
}
