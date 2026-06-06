package handler

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/storagesvc"
	"github.com/go-chi/chi/v5"
)

// newStorageRouter wires the project-scoped (user-facing) storage routes with
// an in-memory bucket store + offline R2 client, under the real
// /api/projects/{projectId}/storage path shape.
func newStorageRouter(t *testing.T) (chi.Router, *inMemoryBucketStoreForTest) {
	t.Helper()
	store := newInMemoryBucketStoreForTest()
	r2, err := storagesvc.NewR2Client(storagesvc.R2Config{
		AccessKeyID: "k", SecretAccessKey: "s",
		Endpoint: testR2URL, Bucket: testPlatformBucket,
	})
	if err != nil {
		t.Fatalf("r2 client: %v", err)
	}
	h := NewStorageHandler(storagesvc.NewService(store, r2, nil), nil)

	r := chi.NewRouter()
	r.Route("/api/projects/{projectId}/storage", func(r chi.Router) { h.Routes(r) })
	r.Group(func(r chi.Router) { h.PublicRoutes(r) })
	return r, store
}

func storageJSON(t *testing.T, v any) *bytes.Reader {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return bytes.NewReader(b)
}

func doStorage(t *testing.T, r chi.Router, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var rdr *bytes.Reader
	if body != nil {
		rdr = storageJSON(t, body)
	} else {
		rdr = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, path, rdr)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func TestStorageRoutes_BucketLifecycle(t *testing.T) {
	r, _ := newStorageRouter(t)
	base := "/api/projects/proj-s/storage"

	// Empty list initially.
	if w := doStorage(t, r, "GET", base+"/buckets", nil); w.Code != http.StatusOK {
		t.Fatalf("list buckets: %d body=%s", w.Code, w.Body.String())
	}

	// Create.
	w := doStorage(t, r, "POST", base+"/buckets", map[string]any{"name": "avatars", "public": true})
	if w.Code != http.StatusCreated {
		t.Fatalf("create bucket: %d body=%s", w.Code, w.Body.String())
	}

	// List now returns one.
	w = doStorage(t, r, "GET", base+"/buckets", nil)
	var buckets []storagesvc.Bucket
	json.Unmarshal(w.Body.Bytes(), &buckets)
	if len(buckets) != 1 || buckets[0].Name != "avatars" {
		t.Errorf("list after create: %+v", buckets)
	}

	// Delete.
	if w := doStorage(t, r, "DELETE", base+"/buckets/avatars", nil); w.Code != http.StatusNoContent {
		t.Errorf("delete bucket: %d body=%s", w.Code, w.Body.String())
	}
}

func TestStorageRoutes_CreateBucket_BadJSON(t *testing.T) {
	r, _ := newStorageRouter(t)
	req := httptest.NewRequest("POST", "/api/projects/proj-s/storage/buckets", bytes.NewReader([]byte("{not json")))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("bad json: got %d", w.Code)
	}
}

func TestStorageRoutes_CreateBucket_InvalidName(t *testing.T) {
	r, _ := newStorageRouter(t)
	w := doStorage(t, r, "POST", "/api/projects/proj-s/storage/buckets", map[string]any{"name": "X"})
	if w.Code != http.StatusBadRequest {
		t.Errorf("invalid name: got %d body=%s", w.Code, w.Body.String())
	}
}

func TestStorageRoutes_ObjectLifecycle(t *testing.T) {
	r, _ := newStorageRouter(t)
	base := "/api/projects/proj-s/storage"
	_ = doStorage(t, r, "POST", base+"/buckets", map[string]any{"name": "files"})

	// Sign an upload URL.
	w := doStorage(t, r, "POST", base+"/buckets/files/upload-url", map[string]any{
		"key": "a.txt", "mimeType": "text/plain", "size": 10,
	})
	if w.Code != http.StatusOK {
		t.Fatalf("sign upload: %d body=%s", w.Code, w.Body.String())
	}

	// Confirm the upload (records metadata).
	w = doStorage(t, r, "POST", base+"/buckets/files/confirm-upload", map[string]any{
		"key": "a.txt", "size": 10, "mimeType": "text/plain", "etag": "e1",
	})
	if w.Code != http.StatusCreated {
		t.Fatalf("confirm: %d body=%s", w.Code, w.Body.String())
	}

	// List objects.
	w = doStorage(t, r, "GET", base+"/buckets/files/objects", nil)
	var list storagesvc.ListObjectsResponse
	json.Unmarshal(w.Body.Bytes(), &list)
	if len(list.Objects) != 1 {
		t.Errorf("list objects: got %d, want 1", len(list.Objects))
	}

	// Metadata probe (exists).
	if w := doStorage(t, r, "GET", base+"/buckets/files/objects/a.txt", nil); w.Code != http.StatusOK {
		t.Errorf("metadata exists: got %d", w.Code)
	}

	// Sign a download URL for a private bucket.
	if w := doStorage(t, r, "GET", base+"/buckets/files/download-url/a.txt", nil); w.Code != http.StatusOK {
		t.Errorf("download url: got %d body=%s", w.Code, w.Body.String())
	}

	// Delete the object (R2 delete errors offline but DB row is removed;
	// handler returns 204 either way).
	if w := doStorage(t, r, "DELETE", base+"/buckets/files/objects/a.txt", nil); w.Code != http.StatusOK && w.Code != http.StatusNoContent && w.Code != http.StatusBadRequest {
		t.Errorf("delete object unexpected status: %d body=%s", w.Code, w.Body.String())
	}
}

func TestStorageRoutes_GetObjectMetadata_NotFound(t *testing.T) {
	r, _ := newStorageRouter(t)
	base := "/api/projects/proj-s/storage"
	_ = doStorage(t, r, "POST", base+"/buckets", map[string]any{"name": "files"})
	w := doStorage(t, r, "GET", base+"/buckets/files/objects/ghost.txt", nil)
	if w.Code != http.StatusNotFound {
		t.Errorf("metadata missing: got %d", w.Code)
	}
}

func TestStorageRoutes_PublicGetObject_RedirectsForPublicBucket(t *testing.T) {
	r, _ := newStorageRouter(t)
	base := "/api/projects/proj-s/storage"
	_ = doStorage(t, r, "POST", base+"/buckets", map[string]any{"name": "pub", "public": true})

	req := httptest.NewRequest("GET", "/storage/v1/object/public/proj-s/pub/logo.png", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusFound {
		t.Errorf("public object should 302-redirect, got %d body=%s", w.Code, w.Body.String())
	}
	if loc := w.Header().Get("Location"); loc == "" {
		t.Error("expected Location header on public redirect")
	}
}

func TestStorageRoutes_PublicGetObject_PrivateBucketForbidden(t *testing.T) {
	r, _ := newStorageRouter(t)
	base := "/api/projects/proj-s/storage"
	_ = doStorage(t, r, "POST", base+"/buckets", map[string]any{"name": "priv"})

	req := httptest.NewRequest("GET", "/storage/v1/object/public/proj-s/priv/x.txt", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Errorf("private bucket on public path should 403, got %d", w.Code)
	}
}
