package storagesvc

import (
	"strings"
	"testing"
)

const (
	testProjABC        = "proj-abc"
	testBaseURL        = "https://x"
	testPlatformBucket = "platform-bucket"
)

// TestObjectKey_BuildsExpectedPrefix pins the storage layout. Changing
// this changes where every existing object lives — caller-of-last-resort
// to update both server + frontend.
func TestObjectKey_BuildsExpectedPrefix(t *testing.T) {
	cases := []struct {
		project, bucket, key, want string
	}{
		{testProjABC, "avatars", "u/123.png", "projects/proj-abc/buckets/avatars/u/123.png"},
		{testProjABC, "avatars", "/u/123.png", "projects/proj-abc/buckets/avatars/u/123.png"},
		{testProjABC, "files", "doc.pdf", "projects/proj-abc/buckets/files/doc.pdf"},
		// path.Clean normalises double slashes
		{testProjABC, "files", "//deeply//nested/file.txt", "projects/proj-abc/buckets/files/deeply/nested/file.txt"},
	}
	for _, c := range cases {
		got, err := objectKey(c.project, c.bucket, c.key)
		if err != nil {
			t.Errorf("objectKey(%q,%q,%q): %v", c.project, c.bucket, c.key, err)
			continue
		}
		if got != c.want {
			t.Errorf("objectKey(%q,%q,%q): got %q, want %q", c.project, c.bucket, c.key, got, c.want)
		}
	}
}

// TestObjectKey_RejectsTraversal covers the security boundary. Key prefixes
// keep one project's files invisible to another; ".." in user-supplied
// keys would let an attacker escape into another project's prefix.
func TestObjectKey_RejectsTraversal(t *testing.T) {
	bad := []string{
		"../escape",
		"foo/../../escape",
		"foo/../bar",
		"",
		"/",
	}
	for _, k := range bad {
		_, err := objectKey("proj-x", "bucket", k)
		if err == nil {
			t.Errorf("objectKey(proj-x, bucket, %q) should fail", k)
		}
	}
}

// TestObjectKey_RequiresProjectAndBucket — service code never calls with
// these empty, but tests catch regressions if a refactor drops a check.
func TestObjectKey_RequiresProjectAndBucket(t *testing.T) {
	if _, err := objectKey("", "bucket", "key"); err == nil {
		t.Error("empty project should fail")
	}
	if _, err := objectKey("proj", "", "key"); err == nil {
		t.Error("empty bucket should fail")
	}
}

// TestNewR2Client_RejectsMissingConfig — the client is constructed once
// at startup; if creds aren't set we want a hard failure at process boot,
// not a 500 on the first request.
func TestNewR2Client_RejectsMissingConfig(t *testing.T) {
	cases := []struct {
		name string
		cfg  R2Config
	}{
		{"missing key", R2Config{SecretAccessKey: "x", Endpoint: testBaseURL, Bucket: "b"}},
		{"missing secret", R2Config{AccessKeyID: "x", Endpoint: testBaseURL, Bucket: "b"}},
		{"missing endpoint", R2Config{AccessKeyID: "x", SecretAccessKey: "x", Bucket: "b"}},
		{"missing bucket", R2Config{AccessKeyID: "x", SecretAccessKey: "x", Endpoint: testBaseURL}},
	}
	for _, c := range cases {
		_, err := NewR2Client(c.cfg)
		if err == nil {
			t.Errorf("%s: expected error", c.name)
		}
	}
}

// TestPublicURL_UsesCustomDomainWhenSet — public objects should be served
// from the operator's custom domain (files.excalibase.io) when configured,
// for branded URLs and CDN cache hits.
func TestPublicURL_UsesCustomDomainWhenSet(t *testing.T) {
	c, err := NewR2Client(R2Config{
		AccessKeyID: "k", SecretAccessKey: "s",
		Endpoint:  "https://acct.r2.cloudflarestorage.com",
		Bucket:    testPlatformBucket,
		PublicURL: "https://files.excalibase.io",
	})
	if err != nil {
		t.Fatalf("NewR2Client: %v", err)
	}
	got, err := c.PublicURL(testProjABC, "avatars", "u/1.png")
	if err != nil {
		t.Fatalf("PublicURL: %v", err)
	}
	want := "https://files.excalibase.io/projects/proj-abc/buckets/avatars/u/1.png"
	if got != want {
		t.Errorf("PublicURL: got %q, want %q", got, want)
	}
}

// TestPublicURL_FallsBackWhenNoCustomDomain ensures URLs are at least
// constructible (even if the operator hasn't enabled R2 public access);
// the URL won't actually serve until the bucket is opened, but the
// string format is stable and the test pins it.
func TestPublicURL_FallsBackWhenNoCustomDomain(t *testing.T) {
	c, _ := NewR2Client(R2Config{
		AccessKeyID: "k", SecretAccessKey: "s",
		Endpoint: "https://acct.r2.cloudflarestorage.com",
		Bucket:   testPlatformBucket,
	})
	got, err := c.PublicURL(testProjABC, "files", "x.txt")
	if err != nil {
		t.Fatalf("PublicURL: %v", err)
	}
	if !strings.Contains(got, testPlatformBucket) || !strings.Contains(got, "projects/proj-abc/buckets/files/x.txt") {
		t.Errorf("fallback URL malformed: %s", got)
	}
}
