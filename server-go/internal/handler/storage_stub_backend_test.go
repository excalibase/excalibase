package handler

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/storagesvc"
)

// storageBackendStub is an S3 stand-in for the storage handler tests. Confirm
// now reads the object back before recording it, so the tests need a blob
// plane that can be told what it holds — an unreachable endpoint can only
// ever answer "failed".
type storageBackendStub struct {
	mu      sync.Mutex
	objects map[string]stubStoredObject
	deleted []string
}

type stubStoredObject struct {
	size     int64
	mimeType string
}

func newStorageBackendStub() *storageBackendStub {
	return &storageBackendStub{objects: map[string]stubStoredObject{}}
}

// put makes the stub hold key with the given size and content type.
func (b *storageBackendStub) put(key string, size int64, mimeType string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.objects[key] = stubStoredObject{size: size, mimeType: mimeType}
}

func (b *storageBackendStub) lookup(path string) (stubStoredObject, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for key, obj := range b.objects {
		if strings.HasSuffix(path, "/"+key) {
			return obj, true
		}
	}
	return stubStoredObject{}, false
}

func (b *storageBackendStub) deletedKeys() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]string(nil), b.deleted...)
}

func (b *storageBackendStub) handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodHead:
			obj, ok := b.lookup(r.URL.Path)
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			w.Header().Set("Content-Length", strconv.FormatInt(obj.size, 10))
			w.Header().Set("Content-Type", obj.mimeType)
			w.Header().Set("ETag", `"stub-etag"`)
			w.WriteHeader(http.StatusOK)
		case http.MethodDelete:
			b.mu.Lock()
			b.deleted = append(b.deleted, r.URL.Path)
			b.mu.Unlock()
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusOK)
		}
	}
}

// newStubbedStorageService wires a storage service onto the stub backend.
func newStubbedStorageService(t *testing.T, store storagesvc.BucketStore, backend *storageBackendStub, quota map[string]int64) *storagesvc.Service {
	t.Helper()
	srv := httptest.NewServer(backend.handler())
	t.Cleanup(srv.Close)
	r2, err := storagesvc.NewR2Client(storagesvc.R2Config{
		AccessKeyID: "k", SecretAccessKey: "s",
		Endpoint: srv.URL, Bucket: testPlatformBucket,
	})
	if err != nil {
		t.Fatalf("r2 client: %v", err)
	}
	return storagesvc.NewService(store, r2, quota)
}
