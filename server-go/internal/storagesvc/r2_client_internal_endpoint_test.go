package storagesvc

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// A single host (EXC-578) signs links for the address browsers use while its
// own calls go to the store over the internal network: the public address is
// not reachable from inside the platform.
func TestR2_InternalEndpointTakesServerCallsAndLinksStayPublic(t *testing.T) {
	var internalCalls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		internalCalls++
		w.Header().Set("Content-Length", "5")
		w.Header().Set("Content-Type", "text/plain")
		w.Header().Set("ETag", `"abc"`)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	c, err := NewR2Client(R2Config{
		AccessKeyID: "k", SecretAccessKey: "s", Bucket: testPlatformBucket,
		Endpoint:         "https://files.example.test:8443",
		InternalEndpoint: srv.URL,
	})
	if err != nil {
		t.Fatalf("NewR2Client: %v", err)
	}

	put, _, err := c.SignedPutURL(context.Background(), testProjABC, "assets", "a.txt", "text/plain", 5, 0)
	if err != nil {
		t.Fatalf("SignedPutURL: %v", err)
	}
	get, _, err := c.SignedGetURL(context.Background(), testProjABC, "assets", "a.txt", false, 0)
	if err != nil {
		t.Fatalf("SignedGetURL: %v", err)
	}
	for _, link := range []string{put, get} {
		parsed, err := url.Parse(link)
		if err != nil || parsed.Host != "files.example.test:8443" || !strings.HasPrefix(parsed.Path, "/"+testPlatformBucket+"/") {
			t.Errorf("link %q is not signed for the public address", link)
		}
	}

	stat, err := c.HeadObject(context.Background(), testProjABC, "assets", "a.txt")
	if err != nil {
		t.Fatalf("HeadObject through the internal endpoint: %v", err)
	}
	if internalCalls != 1 || stat.Size != 5 {
		t.Errorf("internal calls = %d, size = %d; want 1 call reaching the internal endpoint", internalCalls, stat.Size)
	}
}
