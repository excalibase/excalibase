package handler

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

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
	written  time.Time
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

// copy mirrors the store's server-side copy. The stub holds bucket-relative
// keys, so the destination is whatever follows "/buckets/<bucketId>/".
func (b *storageBackendStub) copy(source, destinationPath string) {
	decoded, err := url.PathUnescape(source)
	if err != nil {
		decoded = source
	}
	obj, ok := b.lookup(decoded)
	if !ok {
		return
	}
	_, after, found := strings.Cut(destinationPath, "/buckets/")
	if !found {
		return
	}
	_, key, found := strings.Cut(after, "/")
	if !found || key == "" {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.objects[key] = obj
}

// writeListing answers ListObjectsV2 over the bucket-relative keys the stub
// holds, matching only the part of the prefix that reaches inside the bucket.
func (b *storageBackendStub) writeListing(w http.ResponseWriter, prefix string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	relative := ""
	if _, after, found := strings.Cut(prefix, "/buckets/"); found {
		_, relative, _ = strings.Cut(after, "/")
	}
	base := strings.TrimSuffix(prefix, relative)
	var body strings.Builder
	body.WriteString(`<ListBucketResult xmlns="http://s3.amazonaws.com/doc/2006-03-01/">`)
	for key, obj := range b.objects {
		if !strings.HasPrefix(key, relative) {
			continue
		}
		fmt.Fprintf(&body, "<Contents><Key>%s</Key><Size>%d</Size><LastModified>%s</LastModified></Contents>",
			base+key, obj.size, obj.written.UTC().Format(time.RFC3339))
	}
	body.WriteString(`</ListBucketResult>`)
	w.Header().Set("Content-Type", "application/xml")
	_, _ = w.Write([]byte(body.String()))
}

func (b *storageBackendStub) deletedKeys() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]string(nil), b.deleted...)
}

func (b *storageBackendStub) handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if source := r.Header.Get("X-Amz-Copy-Source"); source != "" {
			b.copy(source, r.URL.Path)
			w.Header().Set("Content-Type", "application/xml")
			_, _ = w.Write([]byte(`<CopyObjectResult><ETag>"stub-etag"</ETag></CopyObjectResult>`))
			return
		}
		if r.Method == http.MethodGet && r.URL.Query().Get("list-type") == "2" {
			b.writeListing(w, r.URL.Query().Get("prefix"))
			return
		}
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
			if !obj.written.IsZero() {
				w.Header().Set("Last-Modified", obj.written.Format(http.TimeFormat))
			}
			w.WriteHeader(http.StatusOK)
		case http.MethodDelete:
			b.mu.Lock()
			b.deleted = append(b.deleted, r.URL.Path)
			for key := range b.objects {
				if strings.HasSuffix(r.URL.Path, "/"+key) {
					delete(b.objects, key)
					break
				}
			}
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
