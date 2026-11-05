package appregistry

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// pushRegistry stores what is pushed, as a registry would, with switches for misbehaving.
type pushRegistry struct {
	mu        sync.Mutex
	blobs     map[string][]byte
	manifests map[string][]byte
	location  string
	failPut   bool
	digest    string
}

func (p *pushRegistry) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	p.mu.Lock()
	defer p.mu.Unlock()
	path := strings.TrimPrefix(r.URL.Path, "/v2/proj-a/api/")
	switch {
	case r.Method == http.MethodHead && strings.HasPrefix(path, "blobs/"):
		if _, ok := p.blobs[strings.TrimPrefix(path, "blobs/")]; ok {
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	case r.Method == http.MethodPost && path == "blobs/uploads/":
		location := p.location
		if location == "" {
			location = "/v2/proj-a/api/blobs/uploads/u1?state=x"
		}
		w.Header().Set("Location", location)
		w.WriteHeader(http.StatusAccepted)
	case r.Method == http.MethodPut && strings.HasPrefix(path, "blobs/uploads/"):
		body, _ := io.ReadAll(r.Body)
		if r.URL.Query().Get("state") != "x" || p.failPut {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		p.blobs[r.URL.Query().Get("digest")] = body
		w.WriteHeader(http.StatusCreated)
	case r.Method == http.MethodPut && strings.HasPrefix(path, "manifests/"):
		body, _ := io.ReadAll(r.Body)
		p.manifests[strings.TrimPrefix(path, "manifests/")] = body
		if p.digest != "" {
			w.Header().Set("Docker-Content-Digest", p.digest)
		}
		w.WriteHeader(http.StatusCreated)
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func pushFixture(t *testing.T) (*Client, *pushRegistry, *Layout) {
	t.Helper()
	registry := &pushRegistry{blobs: map[string][]byte{}, manifests: map[string][]byte{}}
	server := httptest.NewServer(registry)
	t.Cleanup(server.Close)
	issuer, _ := testIssuer(t)
	client, err := NewClient(server.URL, issuer)
	if err != nil {
		t.Fatal(err)
	}
	entries, root := image(t, "amd64", nil)
	layout, err := ReadLayout(bytes.NewReader(tarOf(t, append([]entry{layoutFile, indexEntry(t, root)}, entries...)...)), t.TempDir(), 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	return client, registry, layout
}

func TestPush_UploadsTheBlobsThenTagsTheRoot(t *testing.T) {
	client, registry, layout := pushFixture(t)
	at := time.Unix(1_800_000_000, 0)
	digest, err := client.Push(context.Background(), "proj-a/api", layout, at)
	if err != nil {
		t.Fatal(err)
	}
	tag, _ := PushTag(digest, at)
	if digest != string(layout.Root.Digest) || len(registry.blobs) != 2 || registry.manifests[tag] == nil {
		t.Fatalf("digest %s blobs %d manifests %v", digest, len(registry.blobs), registry.manifests)
	}
	// A second push uploads no blob the registry holds.
	registry.failPut = true
	if _, err := client.Push(context.Background(), "proj-a/api", layout, at); err != nil {
		t.Errorf("a push of what is there: %v", err)
	}
}

func TestPush_RefusesARegistryThatMisbehaves(t *testing.T) {
	client, registry, layout := pushFixture(t)
	registry.location = "http://elsewhere.example/v2/proj-a/api/blobs/uploads/u1?state=x"
	if _, err := client.Push(context.Background(), "proj-a/api", layout, time.Now()); err == nil {
		t.Error("an upload sent to another host")
	}
	client, registry, layout = pushFixture(t)
	registry.failPut = true
	if _, err := client.Push(context.Background(), "proj-a/api", layout, time.Now()); err == nil {
		t.Error("a refused upload")
	}
	client, registry, layout = pushFixture(t)
	registry.digest = "sha256:" + strings.Repeat("0", 64)
	if _, err := client.Push(context.Background(), "proj-a/api", layout, time.Now()); err == nil {
		t.Error("a registry that stored other bytes")
	}
	if _, err := client.Push(context.Background(), "proj-a/../x", layout, time.Now()); err == nil {
		t.Error("a repository outside a project")
	}
}
