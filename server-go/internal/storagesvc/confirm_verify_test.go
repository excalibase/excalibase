package storagesvc

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// EXC-405, confirm half — the client's word about what it uploaded is not
// evidence. An S3 stub stands in for the blob plane so the confirm path can
// be shown what is really stored: a 5 MiB object on a 1 KiB quota, or a
// binary on an image-only bucket.

// stubbedObjectBackend is an S3 stand-in that holds a known set of objects.
// It answers HEAD from that set, so a test can say what the store really
// contains — a 5 MiB payload, a binary, or nothing at all — and records the
// keys that were deleted.
type stubbedObjectBackend struct {
	mu      sync.Mutex
	objects map[string]stubObject // user key → what the store holds
	deleted []string
}

type stubObject struct {
	size         int64
	mimeType     string
	lastModified time.Time
}

func newStubBackend() *stubbedObjectBackend {
	return &stubbedObjectBackend{objects: map[string]stubObject{}}
}

// put makes the store hold key with the given size and content type, written
// just now.
func (b *stubbedObjectBackend) put(key string, size int64, mimeType string) {
	b.putAt(key, size, mimeType, time.Now().UTC())
}

// putAt is put with an explicit write time, for ageing an object past the
// reaper's grace period.
func (b *stubbedObjectBackend) putAt(key string, size int64, mimeType string, written time.Time) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.objects[key] = stubObject{size: size, mimeType: mimeType, lastModified: written}
}

func (b *stubbedObjectBackend) lookup(path string) (stubObject, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for key, obj := range b.objects {
		if strings.HasSuffix(path, "/"+key) {
			return obj, true
		}
	}
	return stubObject{}, false
}

func (b *stubbedObjectBackend) handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Query().Get("list-type") == "2" {
			b.writeListing(w, r.URL.Query().Get("prefix"))
			return
		}
		if source := r.Header.Get("X-Amz-Copy-Source"); source != "" {
			b.copy(source, r.URL.Path)
			w.Header().Set("Content-Type", "application/xml")
			_, _ = w.Write([]byte(`<CopyObjectResult><ETag>"real-etag"</ETag></CopyObjectResult>`))
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
			w.Header().Set("ETag", `"real-etag"`)
			w.WriteHeader(http.StatusOK)
		case http.MethodDelete:
			b.mu.Lock()
			b.deleted = append(b.deleted, r.URL.Path)
			b.mu.Unlock()
			b.deleteKey(r.URL.Path)
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusOK)
		}
	}
}

// writeListing answers ListObjectsV2. The stub holds bucket-relative keys
// while the request's prefix is a whole store key, so only the part of the
// prefix that reaches inside the bucket is matched against them.
func (b *stubbedObjectBackend) writeListing(w http.ResponseWriter, prefix string) {
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
			base+key, obj.size, obj.lastModified.UTC().Format(time.RFC3339))
	}
	body.WriteString(`</ListBucketResult>`)
	w.Header().Set("Content-Type", "application/xml")
	_, _ = w.Write([]byte(body.String()))
}

// copy mirrors the store's server-side copy: the destination ends up holding
// what the source held.
func (b *stubbedObjectBackend) copy(source, destinationPath string) {
	decoded, err := url.PathUnescape(source)
	if err != nil {
		decoded = source
	}
	obj, ok := b.lookup(decoded)
	if !ok {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	// The stub holds keys relative to their bucket, so the destination is
	// whatever follows "/buckets/<bucketId>/" in the request path.
	_, after, found := strings.Cut(destinationPath, "/buckets/")
	if !found {
		return
	}
	_, key, found := strings.Cut(after, "/")
	if !found || key == "" {
		return
	}
	b.objects[key] = obj
}

// deleteKey removes an object from the stub, mirroring what the real store
// does when the service deletes it.
func (b *stubbedObjectBackend) deleteKey(path string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for key := range b.objects {
		if strings.HasSuffix(path, "/"+key) {
			delete(b.objects, key)
			return
		}
	}
}

func (b *stubbedObjectBackend) deletedKeys() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]string(nil), b.deleted...)
}

// serviceOverStub wires a Service whose blob plane is the stub backend.
func serviceOverStub(t *testing.T, store BucketStore, backend *stubbedObjectBackend, quota map[string]int64) *Service {
	t.Helper()
	srv := httptest.NewServer(backend.handler())
	t.Cleanup(srv.Close)
	r2, err := NewR2Client(R2Config{
		AccessKeyID: "k", SecretAccessKey: "s",
		Endpoint: srv.URL, Bucket: testPlatformBucket,
	})
	if err != nil {
		t.Fatalf("NewR2Client: %v", err)
	}
	return NewService(store, r2, quota)
}

func newStubbedService(t *testing.T, backend *stubbedObjectBackend, quota map[string]int64) (*Service, *memStore) {
	t.Helper()
	store := newMemStore()
	return serviceOverStub(t, store, backend, quota), store
}

// The finding's "confirm with size:0": the object is 5 MiB, the project's cap
// is 1 KiB. Quota must be charged what is really there, and the over-quota
// object removed.
func TestService_ConfirmUpload_EnforcesRealSize(t *testing.T) {
	backend := newStubBackend()
	backend.put(stagingObjectKey(testUploadID("payload")), 5*1024*1024, "image/png")
	svc, store := newStubbedService(t, backend, map[string]int64{"free": 1024})
	ctx := context.Background()
	_, _ = svc.CreateBucket(ctx, testProjX, CreateBucketRequest{Name: "images"})

	_, err := svc.ConfirmUpload(ctx, testProjX, "images", "FREE", "u", ConfirmUploadRequest{
		Key: "payload", UploadID: testUploadID("payload"),
	})
	if err == nil {
		t.Fatal("a 5 MiB object on a 1 KiB quota must be refused whatever the caller claims")
	}
	if used, _ := store.GetQuotaBytes(ctx, testProjX); used != 0 {
		t.Errorf("quota must not be charged for a refused upload, got %d", used)
	}
	if len(backend.deletedKeys()) == 0 {
		t.Error("the over-quota object must be deleted from the object store")
	}
}

// A binary uploaded to an image-only bucket is caught on confirm even when
// the caller claims an allowed type.
func TestService_ConfirmUpload_EnforcesRealContentType(t *testing.T) {
	backend := newStubBackend()
	backend.put(stagingObjectKey(testUploadID("payload.png")), 10, "application/x-msdownload")
	svc, store := newStubbedService(t, backend, nil)
	ctx := context.Background()
	_, _ = svc.CreateBucket(ctx, testProjX, CreateBucketRequest{
		Name: "images", AllowedMimeTypes: []string{"image/png"},
	})

	_, err := svc.ConfirmUpload(ctx, testProjX, "images", "FREE", "u", ConfirmUploadRequest{
		Key: "payload.png", UploadID: testUploadID("payload.png"),
	})
	if err == nil || !strings.Contains(err.Error(), "not allowed") {
		t.Fatalf("the stored content type must decide, got %v", err)
	}
	if len(backend.deletedKeys()) == 0 {
		t.Error("the disallowed object must be deleted from the object store")
	}
	out, _ := svc.ListObjects(ctx, testProjX, "images", ListObjectsRequest{})
	if out != nil && len(out.Objects) != 0 {
		t.Error("no catalogue row may be recorded for a refused upload")
	}
	if used, _ := store.GetQuotaBytes(ctx, testProjX); used != 0 {
		t.Errorf("quota must not be charged, got %d", used)
	}
}

// Quota accounting uses the verified size, never the caller's.
func TestService_ConfirmUpload_ChargesVerifiedSize(t *testing.T) {
	backend := newStubBackend()
	backend.put(stagingObjectKey(testUploadID("a.txt")), 4096, "text/plain")
	svc, store := newStubbedService(t, backend, nil)
	ctx := context.Background()
	_, _ = svc.CreateBucket(ctx, testProjX, CreateBucketRequest{Name: "files"})

	obj, err := svc.ConfirmUpload(ctx, testProjX, "files", "FREE", "u", ConfirmUploadRequest{
		Key: "a.txt", UploadID: testUploadID("a.txt"),
	})
	if err != nil {
		t.Fatalf("ConfirmUpload: %v", err)
	}
	if obj.Size != 4096 {
		t.Errorf("recorded size: got %d, want the stored 4096", obj.Size)
	}
	if used, _ := store.GetQuotaBytes(ctx, testProjX); used != 4096 {
		t.Errorf("quota charged: got %d, want 4096", used)
	}
}

// Confirming an upload that never landed records nothing.
func TestService_ConfirmUpload_RejectsMissingObject(t *testing.T) {
	backend := newStubBackend() // holds nothing
	svc, store := newStubbedService(t, backend, nil)
	ctx := context.Background()
	_, _ = svc.CreateBucket(ctx, testProjX, CreateBucketRequest{Name: "files"})

	if _, err := svc.ConfirmUpload(ctx, testProjX, "files", "FREE", "u", ConfirmUploadRequest{
		Key: "ghost.txt", UploadID: testUploadID("ghost.txt"),
	}); err == nil {
		t.Fatal("confirming an object the store does not have must fail")
	}
	if used, _ := store.GetQuotaBytes(ctx, testProjX); used != 0 {
		t.Errorf("quota charged for a missing object: %d", used)
	}
}
