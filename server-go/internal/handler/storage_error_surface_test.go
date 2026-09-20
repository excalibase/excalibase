package handler

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/storage"
	"github.com/excalibase/provisioning-poc/internal/storagesvc"
	"github.com/go-chi/chi/v5"
)

// EXC-404 neighbours: every storage route must distinguish "the caller asked
// for something impossible" from "the platform failed", and must never answer
// success for work that did not happen.

// catalogueFailingStore is the in-memory store with a broken catalogue
// delete — the platform-db-unavailable half of the finding's scenario.
type catalogueFailingStore struct {
	*inMemoryBucketStoreForTest
}

func (c *catalogueFailingStore) DeleteObjectAndReleaseQuota(_ context.Context, _, _, _ string) (bool, error) {
	return false, errors.New("platform db unavailable")
}

func newStorageRouterWith(store storagesvc.BucketStore) (chi.Router, *StorageHandler) {
	svc := storagesvc.NewServiceWithObjectStore(store, newFakeObjectStoreForTest(), nil)
	h := NewStorageHandler(svc, nil)
	h.SetRuntimeSecret("the-secret")
	r := chi.NewRouter()
	r.Route("/api/projects/{projectId}/storage", func(r chi.Router) { h.Routes(r) })
	r.Group(func(r chi.Router) { h.PublicRoutes(r) })
	h.InternalRoutes(r)
	return r, h
}

func TestInternalStorage_Delete_CatalogueFailureIsNot204(t *testing.T) {
	store := &catalogueFailingStore{newInMemoryBucketStoreForTest()}
	r, _ := newStorageRouterWith(store)
	_ = store.CreateBucket(context.Background(), &storagesvc.Bucket{
		ID: "bkt_cat", ProjectID: testStorageProjectID, Name: ctxStorageBucket,
	})
	_ = store.CreateObject(context.Background(), &storagesvc.Object{
		ID: "obj_cat", BucketID: "bkt_cat", Key: "kg2_a", Size: 4,
	})

	req := httptest.NewRequest("DELETE", "/internal/storage/"+testStorageProjectID+"/kg2_a", nil)
	req.Header.Set(runtimeTokenHeader, testStorageRuntimeToken)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("catalogue failure: want 500, got %d (body=%s)", w.Code, w.Body.String())
	}
}

// A storage id whose bucket was never provisioned has nothing left to
// remove, so the delete is complete.
func TestInternalStorage_Delete_UnknownBucketIs204(t *testing.T) {
	r, _ := newStorageRouterWith(newInMemoryBucketStoreForTest())
	req := httptest.NewRequest("DELETE", "/internal/storage/"+testStorageProjectID+"/kg2_ghost", nil)
	req.Header.Set(runtimeTokenHeader, testStorageRuntimeToken)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusNoContent {
		t.Fatalf("unknown bucket: want 204, got %d (body=%s)", w.Code, w.Body.String())
	}
}

func TestInternalStorage_UploadURL_RejectsInvalidBody(t *testing.T) {
	r, _ := newStorageRouterWith(newInMemoryBucketStoreForTest())
	req := httptest.NewRequest("POST", "/internal/storage/"+testStorageProjectID+"/upload-url",
		strings.NewReader("{not json"))
	req.Header.Set(runtimeTokenHeader, testStorageRuntimeToken)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("malformed upload-url body: want 400, got %d (body=%s)", w.Code, w.Body.String())
	}
}

func TestStorageRoutes_DeleteBucket_UnknownBucketIs404(t *testing.T) {
	r, _ := newStorageRouterWith(newInMemoryBucketStoreForTest())
	w := doStorage(t, r, "DELETE", "/api/projects/proj-s/storage/buckets/ghost", nil)
	if w.Code != http.StatusNotFound {
		t.Fatalf("unknown bucket: want 404, got %d (body=%s)", w.Code, w.Body.String())
	}
}

func TestStorageRoutes_CreateBucket_DuplicateIs409(t *testing.T) {
	r, _ := newStorageRouterWith(newInMemoryBucketStoreForTest())
	base := "/api/projects/proj-s/storage/buckets"
	if w := doStorage(t, r, "POST", base, map[string]any{"name": "assets"}); w.Code != http.StatusCreated {
		t.Fatalf("create bucket: %d", w.Code)
	}
	if w := doStorage(t, r, "POST", base, map[string]any{"name": "assets"}); w.Code != http.StatusConflict {
		t.Fatalf("duplicate bucket: want 409, got %d (body=%s)", w.Code, w.Body.String())
	}
}

// A malformed limit is rejected rather than quietly replaced with a default.
func TestStorageRoutes_ListObjects_RejectsNonNumericLimit(t *testing.T) {
	r, _ := newStorageRouterWith(newInMemoryBucketStoreForTest())
	_ = doStorage(t, r, "POST", "/api/projects/proj-s/storage/buckets", map[string]any{"name": "files"})
	w := doStorage(t, r, "GET", "/api/projects/proj-s/storage/buckets/files/objects?limit=lots", nil)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("non-numeric limit: want 400, got %d (body=%s)", w.Code, w.Body.String())
	}
}

// A bucket mid-delete refuses new uploads with a conflict, not a 500.
func TestStorageRoutes_UploadURL_ConflictsWhileBucketDeleting(t *testing.T) {
	store := newInMemoryBucketStoreForTest()
	r, _ := newStorageRouterWith(store)
	_ = doStorage(t, r, "POST", "/api/projects/proj-s/storage/buckets", map[string]any{"name": "assets"})
	if err := store.SetBucketStatus(context.Background(), "proj-s", "assets", storagesvc.BucketStatusDeleting); err != nil {
		t.Fatalf("SetBucketStatus: %v", err)
	}
	w := doStorage(t, r, "POST", "/api/projects/proj-s/storage/buckets/assets/upload-url",
		map[string]any{"key": "a.txt", "mimeType": "text/plain", "size": 5})
	if w.Code != http.StatusConflict {
		t.Fatalf("upload into a deleting bucket: want 409, got %d (body=%s)", w.Code, w.Body.String())
	}
}

// An internal failure must not carry platform detail back to the caller.
func TestStorageRoutes_InternalFailureMessageIsFixed(t *testing.T) {
	r, _ := newStorageRouterWith(&catalogueFailingStore{newInMemoryBucketStoreForTest()})
	base := "/api/projects/proj-s/storage"
	_ = doStorage(t, r, "POST", base+"/buckets", map[string]any{"name": "files"})
	_ = doStorage(t, r, "POST", base+"/buckets/files/confirm-upload",
		map[string]any{"key": "a.txt", "size": 1, "mimeType": "text/plain"})

	w := doStorage(t, r, "DELETE", base+"/buckets/files/objects/a.txt", nil)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("want 500, got %d", w.Code)
	}
	if body := w.Body.String(); !strings.Contains(body, errStorageFailed) || strings.Contains(body, "platform db") {
		t.Errorf("internal detail leaked to the caller: %s", body)
	}
}

// tierLookupFailingStore is an InstanceStore whose project lookup is broken.
// The quota that applies to an upload depends on it, so the request must fail
// rather than proceed under a guessed tier.
type tierLookupFailingStore struct{ errInstanceStore }

func (tierLookupFailingStore) FindByProjectID(string) (*domain.DatabaseInstance, error) {
	return nil, errors.New("platform db unavailable")
}

func newStorageRouterWithInstances(insts storage.InstanceStore) chi.Router {
	svc := storagesvc.NewServiceWithObjectStore(newInMemoryBucketStoreForTest(), newFakeObjectStoreForTest(), nil)
	h := NewStorageHandler(svc, insts)
	h.SetRuntimeSecret("the-secret")
	r := chi.NewRouter()
	r.Route("/api/projects/{projectId}/storage", func(r chi.Router) { h.Routes(r) })
	h.InternalRoutes(r)
	return r
}

func TestStorageRoutes_UploadURL_FailsOnTierLookupFailure(t *testing.T) {
	r := newStorageRouterWithInstances(tierLookupFailingStore{})
	_ = doStorage(t, r, "POST", "/api/projects/proj-s/storage/buckets", map[string]any{"name": "files"})
	w := doStorage(t, r, "POST", "/api/projects/proj-s/storage/buckets/files/upload-url",
		map[string]any{"key": "a.txt", "mimeType": "text/plain", "size": 5})
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("tier lookup failure: want 500, got %d (body=%s)", w.Code, w.Body.String())
	}
}

func TestInternalStorage_UploadURL_FailsOnTierLookupFailure(t *testing.T) {
	r := newStorageRouterWithInstances(tierLookupFailingStore{})
	req := httptest.NewRequest("POST", "/internal/storage/"+testStorageProjectID+"/upload-url",
		strings.NewReader(`{"contentType":"text/plain","size":4}`))
	req.Header.Set(runtimeTokenHeader, testStorageRuntimeToken)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("tier lookup failure: want 500, got %d (body=%s)", w.Code, w.Body.String())
	}
}

// A project with an instance row uses that row's tier for the quota check.
func TestStorageRoutes_UploadURL_UsesProjectTier(t *testing.T) {
	insts := &inMemoryInstanceStore{insts: map[string]*domain.DatabaseInstance{
		"proj-s": {ProjectID: "proj-s", Tier: domain.Free},
	}}
	r := newStorageRouterWithInstances(insts)
	_ = doStorage(t, r, "POST", "/api/projects/proj-s/storage/buckets", map[string]any{"name": "files"})
	w := doStorage(t, r, "POST", "/api/projects/proj-s/storage/buckets/files/upload-url",
		map[string]any{"key": "a.txt", "mimeType": "text/plain", "size": 5})
	if w.Code != http.StatusOK {
		t.Fatalf("upload-url: want 200, got %d (body=%s)", w.Code, w.Body.String())
	}
}

// lookupFailingStore breaks every bucket read, so each route that loads a
// bucket has to answer 5xx rather than "not found" or a success.
type lookupFailingStore struct {
	*inMemoryBucketStoreForTest
}

func (lookupFailingStore) GetBucket(context.Context, string, string) (*storagesvc.Bucket, error) {
	return nil, errors.New("platform db unavailable")
}

func (lookupFailingStore) ListBuckets(context.Context, string) ([]storagesvc.Bucket, error) {
	return nil, errors.New("platform db unavailable")
}

func TestStorageRoutes_BucketLookupFailuresAre5xx(t *testing.T) {
	r, _ := newStorageRouterWith(lookupFailingStore{newInMemoryBucketStoreForTest()})
	base := "/api/projects/proj-s/storage"
	cases := []struct {
		name, method, path string
		body               any
	}{
		{"list buckets", "GET", base + "/buckets", nil},
		{"list objects", "GET", base + "/buckets/files/objects", nil},
		{"confirm upload", "POST", base + "/buckets/files/confirm-upload", map[string]any{"key": "a.txt", "uploadId": "upl_1"}},
		{"download url", "GET", base + "/buckets/files/download-url/a.txt", nil},
		{"object metadata", "GET", base + "/buckets/files/objects/a.txt", nil},
		{"public object", "GET", "/storage/v1/object/public/proj-s/files/a.txt", nil},
		{"upload url", "POST", base + "/buckets/files/upload-url", map[string]any{"key": "a.txt", "size": 1}},
	}
	for _, c := range cases {
		w := doStorage(t, r, c.method, c.path, c.body)
		if w.Code != http.StatusInternalServerError {
			t.Errorf("%s: want 500, got %d (body=%s)", c.name, w.Code, w.Body.String())
		}
	}
}

func TestInternalStorage_BucketLookupFailuresAre5xx(t *testing.T) {
	r, _ := newStorageRouterWith(lookupFailingStore{newInMemoryBucketStoreForTest()})
	type call struct {
		name, method, path, body string
	}
	cases := []call{
		{"upload url", "POST", "/internal/storage/" + testStorageProjectID + "/upload-url", `{"contentType":"text/plain","size":4}`},
		{"confirm upload", "POST", "/internal/storage/" + testStorageProjectID + "/confirm-upload", `{"storageId":"kg2_a","uploadId":"upl_1"}`},
		{"download url", "POST", "/internal/storage/" + testStorageProjectID + "/download-url", `{"storageId":"kg2_a"}`},
		{"metadata", "GET", "/internal/storage/" + testStorageProjectID + "/metadata/kg2_a", ""},
	}
	for _, c := range cases {
		req := httptest.NewRequest(c.method, c.path, strings.NewReader(c.body))
		req.Header.Set(runtimeTokenHeader, testStorageRuntimeToken)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != http.StatusInternalServerError {
			t.Errorf("%s: want 500, got %d (body=%s)", c.name, w.Code, w.Body.String())
		}
	}
}
