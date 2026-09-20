package storagesvc

import (
	"context"
	"net/url"
	"strings"
	"testing"
)

// EXC-405 — omitting upload metadata must not bypass the storage limits.
//
// The finding's scenario: a quota-limited project with an image-only bucket,
// asked for an upload URL with nothing but a key. Quota was checked only for
// a caller-supplied positive size and the MIME check skipped an empty type,
// so the URL was issued and any payload of any size could be uploaded.

func quotaLimitedImageBucket(t *testing.T) (*Service, *memStore) {
	t.Helper()
	store := newMemStore()
	r2, err := NewR2Client(R2Config{
		AccessKeyID: "k", SecretAccessKey: "s",
		Endpoint: testR2URL, Bucket: testPlatformBucket,
	})
	if err != nil {
		t.Fatalf("NewR2Client: %v", err)
	}
	svc := NewService(store, r2, map[string]int64{"free": 1024})
	if _, err := svc.CreateBucket(context.Background(), testProjX, CreateBucketRequest{
		Name:             "images",
		AllowedMimeTypes: []string{"image/png"},
	}); err != nil {
		t.Fatalf("CreateBucket: %v", err)
	}
	return svc, store
}

func TestService_SignUploadURL_RequiresSize(t *testing.T) {
	svc, _ := quotaLimitedImageBucket(t)
	cases := []struct {
		name string
		size int64
	}{
		{"absent", 0},
		{"negative", -1},
	}
	for _, c := range cases {
		_, err := svc.SignUploadURL(context.Background(), testProjX, "images", "FREE", UploadURLRequest{
			Key: "payload", MimeType: "image/png", Size: c.size,
		})
		if err == nil {
			t.Errorf("size %s: an upload URL must not be issued without a usable size", c.name)
		}
	}
}

func TestService_SignUploadURL_RequiresContentType(t *testing.T) {
	svc, _ := quotaLimitedImageBucket(t)
	_, err := svc.SignUploadURL(context.Background(), testProjX, "images", "FREE", UploadURLRequest{
		Key: "payload", Size: 10,
	})
	if err == nil {
		t.Fatal("an empty MIME must not satisfy a MIME-restricted bucket")
	}
}

// The reproduction verbatim: `{"key":"payload"}` and nothing else.
func TestService_SignUploadURL_RejectsBareKey(t *testing.T) {
	svc, _ := quotaLimitedImageBucket(t)
	_, err := svc.SignUploadURL(context.Background(), testProjX, "images", "FREE", UploadURLRequest{Key: "payload"})
	if err == nil {
		t.Fatal("a request carrying only a key must be rejected, not granted an unrestricted URL")
	}
}

// The signed PUT must bind the length as well as the type, so the URL cannot
// be used to upload something other than what was authorised.
func TestService_SignUploadURL_BindsContentLengthAndType(t *testing.T) {
	svc, _ := quotaLimitedImageBucket(t)
	out, err := svc.SignUploadURL(context.Background(), testProjX, "images", "FREE", UploadURLRequest{
		Key: "a.png", MimeType: "image/png", Size: 512,
	})
	if err != nil {
		t.Fatalf("SignUploadURL: %v", err)
	}
	parsed, err := url.Parse(out.URL)
	if err != nil {
		t.Fatalf("parse signed url: %v", err)
	}
	signedHeaders := parsed.Query().Get("X-Amz-SignedHeaders")
	if !strings.Contains(signedHeaders, "content-length") {
		t.Errorf("signed PUT does not bind content length: %q", signedHeaders)
	}
	if !strings.Contains(signedHeaders, "content-type") {
		t.Errorf("signed PUT does not bind content type: %q", signedHeaders)
	}
	if out.Headers["Content-Length"] != "512" {
		t.Errorf("response must tell the client the bound length, got %q", out.Headers["Content-Length"])
	}
}
