package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/storagesvc"
	"github.com/go-chi/chi/v5"
	tusd "github.com/tus/tusd/v2/pkg/handler"
)

// EXC-405 at the HTTP boundary — the finding's scenario end to end: a
// quota-limited project with an image-only bucket, asked for an upload URL
// with nothing but a key.

func newQuotaLimitedRouter(t *testing.T) (chi.Router, *storageBackendStub, *inMemoryBucketStoreForTest) {
	t.Helper()
	store := newInMemoryBucketStoreForTest()
	backend := newStorageBackendStub()
	svc := newStubbedStorageService(t, store, backend, map[string]int64{"free": 1024})
	h := NewStorageHandler(svc, nil)
	h.SetRuntimeSecret("the-secret")
	r := chi.NewRouter()
	r.Route("/api/projects/{projectId}/storage", func(r chi.Router) { h.Routes(r) })
	h.InternalRoutes(r)

	w := doStorage(t, r, "POST", "/api/projects/proj-s/storage/buckets", map[string]any{
		"name": "images", "allowedMimeTypes": []string{"image/png"},
	})
	if w.Code != http.StatusCreated {
		t.Fatalf("create bucket: %d body=%s", w.Code, w.Body.String())
	}
	return r, backend, store
}

func TestStorageRoutes_UploadURL_RejectsMissingMetadata(t *testing.T) {
	r, _, _ := newQuotaLimitedRouter(t)
	base := "/api/projects/proj-s/storage/buckets/images/upload-url"
	cases := []struct {
		name string
		body map[string]any
	}{
		{"bare key", map[string]any{"key": "payload"}},
		{"no size", map[string]any{"key": "payload", "mimeType": "image/png"}},
		{"zero size", map[string]any{"key": "payload", "mimeType": "image/png", "size": 0}},
		{"negative size", map[string]any{"key": "payload", "mimeType": "image/png", "size": -1}},
		{"no mime", map[string]any{"key": "payload", "size": 10}},
		{"blank mime", map[string]any{"key": "payload", "mimeType": "   ", "size": 10}},
		{"malformed mime", map[string]any{"key": "payload", "mimeType": "image/png x=1", "size": 10}},
		{"disallowed mime", map[string]any{"key": "payload", "mimeType": "application/x-msdownload", "size": 10}},
	}
	for _, c := range cases {
		w := doStorage(t, r, "POST", base, c.body)
		if w.Code != http.StatusBadRequest {
			t.Errorf("%s: want 400, got %d (body=%s)", c.name, w.Code, w.Body.String())
		}
	}
}

// Casing and parameters are not a way around the allow-list, in either
// direction: an allowed type still works when spelled differently.
func TestStorageRoutes_UploadURL_AcceptsEquivalentMIMESpelling(t *testing.T) {
	r, _, _ := newQuotaLimitedRouter(t)
	base := "/api/projects/proj-s/storage/buckets/images/upload-url"
	for _, mimeType := range []string{"image/png", "IMAGE/PNG", "image/png; charset=binary"} {
		w := doStorage(t, r, "POST", base, map[string]any{
			"key": "a.png", "mimeType": mimeType, "size": 16,
		})
		if w.Code != http.StatusOK {
			t.Errorf("mime %q: want 200, got %d (body=%s)", mimeType, w.Code, w.Body.String())
		}
	}
}

// The response tells the client exactly what the signature covers, and the
// URL itself carries both headers.
func TestStorageRoutes_UploadURL_ReturnsBoundHeaders(t *testing.T) {
	r, _, _ := newQuotaLimitedRouter(t)
	w := doStorage(t, r, "POST", "/api/projects/proj-s/storage/buckets/images/upload-url",
		map[string]any{"key": "a.png", "mimeType": "image/png", "size": 16})
	if w.Code != http.StatusOK {
		t.Fatalf("upload-url: %d body=%s", w.Code, w.Body.String())
	}
	var out struct {
		URL     string            `json:"url"`
		Headers map[string]string `json:"headers"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.Headers["Content-Length"] != "16" || out.Headers["Content-Type"] != "image/png" {
		t.Errorf("bound headers not returned: %+v", out.Headers)
	}
	if !strings.Contains(out.URL, "content-length") || !strings.Contains(out.URL, "content-type") {
		t.Errorf("signed URL does not bind both headers: %s", out.URL)
	}
}

// An upload over quota is caught on confirm from what the store holds, not
// from what the caller claims, and the object does not survive.
func TestStorageRoutes_ConfirmUpload_RejectsOversizedObject(t *testing.T) {
	r, backend, _ := newQuotaLimitedRouter(t)
	backend.put("payload", 5*1024*1024, "image/png")

	w := doStorage(t, r, "POST", "/api/projects/proj-s/storage/buckets/images/confirm-upload",
		map[string]any{"key": "payload", "size": 0, "mimeType": "image/png"})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("confirm of an over-quota object: want 400, got %d (body=%s)", w.Code, w.Body.String())
	}
	if len(backend.deletedKeys()) == 0 {
		t.Error("the refused object must be deleted from the object store")
	}
	list := doStorage(t, r, "GET", "/api/projects/proj-s/storage/buckets/images/objects", nil)
	if strings.Contains(list.Body.String(), "payload") {
		t.Errorf("a refused upload must leave no catalogue row: %s", list.Body.String())
	}
}

// The runtime's internal route is held to the same contract.
func TestInternalStorage_UploadURL_RejectsMissingMetadata(t *testing.T) {
	r, _, _ := newQuotaLimitedRouter(t)
	for _, body := range []string{`{}`, `{"contentType":"text/plain"}`, `{"size":10}`, `{"contentType":"","size":10}`} {
		req := httptest.NewRequest("POST", "/internal/storage/"+testStorageProjectID+"/upload-url",
			strings.NewReader(body))
		req.Header.Set(runtimeTokenHeader, testStorageRuntimeToken)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != http.StatusBadRequest {
			t.Errorf("body %s: want 400, got %d (body=%s)", body, w.Code, w.Body.String())
		}
	}
}

// Resumable (multipart) uploads go through the same checks: the tus create
// callback refuses an upload with no declared type, and completion records
// what the object store actually holds.
func TestTus_Create_RequiresContentType(t *testing.T) {
	_, h, store, _ := newTusStorageRouterWithBackend(t)
	_ = store.CreateBucket(context.Background(), &storagesvc.Bucket{ID: "b1", ProjectID: "proj1", Name: "media"})

	ctx := context.WithValue(context.Background(), tusProjectCtxKey, "proj1")
	_, _, err := h.prepareTusUpload(tusd.HookEvent{
		Context: ctx,
		Upload: tusd.FileInfo{
			Size:     123,
			MetaData: tusd.MetaData{"bucket": "media", "key": "clips/a.mp4"},
		},
	})
	if err == nil {
		t.Fatal("a resumable upload with no declared type must be refused")
	}
}

func TestTus_Create_RequiresDeclaredLength(t *testing.T) {
	_, h, store, _ := newTusStorageRouterWithBackend(t)
	_ = store.CreateBucket(context.Background(), &storagesvc.Bucket{ID: "b1", ProjectID: "proj1", Name: "media"})

	ctx := context.WithValue(context.Background(), tusProjectCtxKey, "proj1")
	_, _, err := h.prepareTusUpload(tusd.HookEvent{
		Context: ctx,
		Upload: tusd.FileInfo{
			Size:     0,
			MetaData: tusd.MetaData{"bucket": "media", "key": "clips/a.mp4", "filetype": "video/mp4"},
		},
	})
	if err == nil {
		t.Fatal("a resumable upload with no declared length must be refused")
	}
}

// What tus metadata claims does not decide what is recorded.
func TestTus_RecordUpload_UsesStoredSizeAndType(t *testing.T) {
	_, h, store, backend := newTusStorageRouterWithBackend(t)
	_ = store.CreateBucket(context.Background(), &storagesvc.Bucket{ID: "b1", ProjectID: "proj1", Name: "media"})
	backend.put("clips/a.mp4", 9999, "video/mp4")

	ctx := context.WithValue(context.Background(), tusProjectCtxKey, "proj1")
	if _, err := h.recordTusUpload(tusd.HookEvent{
		Context: ctx,
		Upload: tusd.FileInfo{
			Size:     1, // the claim
			MetaData: tusd.MetaData{"bucket": "media", "key": "clips/a.mp4", "filetype": "text/plain"},
		},
	}); err != nil {
		t.Fatalf("recordTusUpload: %v", err)
	}
	obj, _ := store.GetObject(context.Background(), "b1", "clips/a.mp4")
	if obj == nil {
		t.Fatal("no object recorded")
	}
	if obj.Size != 9999 || obj.MimeType != "video/mp4" {
		t.Errorf("recorded the claim instead of the stored object: %+v", obj)
	}
}

// An upload aimed at a bucket that does not exist is the caller's mistake,
// reported as 404 rather than as a platform failure.
func TestStorageRoutes_UploadURL_UnknownBucketIs404(t *testing.T) {
	r, _, _ := newQuotaLimitedRouter(t)
	w := doStorage(t, r, "POST", "/api/projects/proj-s/storage/buckets/ghost/upload-url",
		map[string]any{"key": "a.png", "mimeType": "image/png", "size": 16})
	if w.Code != http.StatusNotFound {
		t.Fatalf("unknown bucket: want 404, got %d (body=%s)", w.Code, w.Body.String())
	}
}

// Confirming an upload the object store never received is refused, and the
// platform's own wording never reaches the caller.
func TestStorageRoutes_ConfirmUpload_MissingObjectIs400(t *testing.T) {
	r, _, _ := newQuotaLimitedRouter(t)
	w := doStorage(t, r, "POST", "/api/projects/proj-s/storage/buckets/images/confirm-upload",
		map[string]any{"key": "never-uploaded.png", "size": 10, "mimeType": "image/png"})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("confirm of a missing object: want 400, got %d (body=%s)", w.Code, w.Body.String())
	}
}

func TestInternalStorage_ConfirmUpload_MissingObjectIsNot204(t *testing.T) {
	r, _, _ := newQuotaLimitedRouter(t)
	req := httptest.NewRequest("POST", "/internal/storage/"+testStorageProjectID+"/confirm-upload",
		strings.NewReader(`{"storageId":"kg2_never","size":10,"contentType":"image/png"}`))
	req.Header.Set(runtimeTokenHeader, testStorageRuntimeToken)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code == http.StatusNoContent {
		t.Fatal("confirming an upload the store never received must not report success")
	}
}

// An internal failure carries a fixed message, never the platform's own.
func TestStorageRoutes_ConfirmUpload_InternalFailureIsOpaque(t *testing.T) {
	store := newInMemoryBucketStoreForTest()
	backend := newStorageBackendStub()
	svc := newStubbedStorageService(t, quotaFailingStore{store}, backend, map[string]int64{"free": 1024})
	h := NewStorageHandler(svc, nil)
	r := chi.NewRouter()
	r.Route("/api/projects/{projectId}/storage", func(r chi.Router) { h.Routes(r) })
	_ = doStorage(t, r, "POST", "/api/projects/proj-s/storage/buckets", map[string]any{"name": "files"})
	backend.put("a.txt", 10, "text/plain")

	w := doStorage(t, r, "POST", "/api/projects/proj-s/storage/buckets/files/confirm-upload",
		map[string]any{"key": "a.txt"})
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("want 500, got %d (body=%s)", w.Code, w.Body.String())
	}
	if body := w.Body.String(); !strings.Contains(body, errStorageFailed) || strings.Contains(body, "platform db") {
		t.Errorf("internal detail leaked to the caller: %s", body)
	}
}

// quotaFailingStore breaks the quota read the upload path depends on.
type quotaFailingStore struct {
	*inMemoryBucketStoreForTest
}

func (quotaFailingStore) GetQuotaBytes(context.Context, string) (int64, error) {
	return 0, errors.New("platform db unavailable")
}

func TestInternalStorage_UploadURL_RejectsMalformedBody(t *testing.T) {
	r, _, _ := newQuotaLimitedRouter(t)
	req := httptest.NewRequest("POST", "/internal/storage/"+testStorageProjectID+"/upload-url",
		strings.NewReader("{not json"))
	req.Header.Set(runtimeTokenHeader, testStorageRuntimeToken)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("malformed body: want 400, got %d (body=%s)", w.Code, w.Body.String())
	}
}
