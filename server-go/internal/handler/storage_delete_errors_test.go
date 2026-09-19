package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/storagesvc"
	"github.com/go-chi/chi/v5"
)

// EXC-404 — the HTTP surface must not answer success when the delete did
// not happen. Both routers below run against an object store that refuses
// every call (closed port), which is the finding's "make R2 reject deletes".

func newFailingStorageInternalRouter(t *testing.T) (chi.Router, *inMemoryBucketStoreForTest) {
	t.Helper()
	store := newInMemoryBucketStoreForTest()
	h := NewStorageHandler(storagesvc.NewService(store, newOfflineR2(t), nil), nil)
	h.SetRuntimeSecret("the-secret")
	r := chi.NewRouter()
	h.InternalRoutes(r)
	return r, store
}

// newOfflineR2 points the client at a closed local port so every network
// call fails immediately instead of reaching the internet.
func newOfflineR2(t *testing.T) *storagesvc.R2Client {
	t.Helper()
	r2, err := storagesvc.NewR2Client(storagesvc.R2Config{
		AccessKeyID: "k", SecretAccessKey: "s",
		Endpoint: "http://127.0.0.1:1", Bucket: testPlatformBucket,
	})
	if err != nil {
		t.Fatalf("NewR2Client: %v", err)
	}
	return r2
}

func TestInternalStorage_Delete_FailedObjectStoreDeleteIsNot204(t *testing.T) {
	r, store := newFailingStorageInternalRouter(t)
	// The catalogue row is seeded directly: the point under test is the
	// delete, and the object store this router talks to answers nothing.
	_ = store.CreateBucket(context.Background(), &storagesvc.Bucket{
		ID: "bkt_fail", ProjectID: testStorageProjectID, Name: ctxStorageBucket,
	})
	_ = store.CreateObject(context.Background(), &storagesvc.Object{
		ID: "obj_fail", BucketID: "bkt_fail", Key: "kg2_stuck", Size: 4,
	})

	req := httptest.NewRequest("DELETE",
		"/internal/storage/"+testStorageProjectID+"/kg2_stuck", nil)
	req.Header.Set(runtimeTokenHeader, testStorageRuntimeToken)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("delete with a failing object store: want 500, got %d (body=%s)", w.Code, w.Body.String())
	}
}

func TestStorageRoutes_DeleteBucket_FailedObjectStoreDeleteIsNot204(t *testing.T) {
	svc := storagesvc.NewService(newInMemoryBucketStoreForTest(), newOfflineR2(t), nil)
	h := NewStorageHandler(svc, nil)
	r := chi.NewRouter()
	r.Route("/api/projects/{projectId}/storage", func(r chi.Router) { h.Routes(r) })

	base := "/api/projects/proj-s/storage"
	if w := doStorage(t, r, "POST", base+"/buckets", map[string]any{"name": "assets"}); w.Code != http.StatusCreated {
		t.Fatalf("create bucket: %d body=%s", w.Code, w.Body.String())
	}
	w := doStorage(t, r, "DELETE", base+"/buckets/assets", nil)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("bucket delete with a failing object store: want 500, got %d (body=%s)", w.Code, w.Body.String())
	}
}
