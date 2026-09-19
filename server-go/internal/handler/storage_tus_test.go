package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/storagesvc"
	"github.com/go-chi/chi/v5"
	tusd "github.com/tus/tusd/v2/pkg/handler"
)

// newTusStorageRouter wires the project-scoped storage routes with resumable
// uploads enabled, backed by an in-memory bucket store and an offline R2
// client (the S3 backend is never contacted by these unit tests — creation
// fails in the pre-create callback before any S3 call, and completion is
// exercised by invoking the callback directly).
func newTusStorageRouter(t *testing.T) (chi.Router, *StorageHandler, *inMemoryBucketStoreForTest) {
	t.Helper()
	store := newInMemoryBucketStoreForTest()
	r2, err := storagesvc.NewR2Client(storagesvc.R2Config{
		AccessKeyID: "k", SecretAccessKey: "s",
		Endpoint: testR2URL, Bucket: testPlatformBucket,
	})
	if err != nil {
		t.Fatalf("r2 client: %v", err)
	}
	svc := storagesvc.NewService(store, r2, nil)
	h := NewStorageHandler(svc, nil)
	if err := h.EnableResumableUploads(svc.TusComposer()); err != nil {
		t.Fatalf("enable resumable uploads: %v", err)
	}

	r := chi.NewRouter()
	r.Route("/api/projects/{projectId}/storage", func(r chi.Router) { h.Routes(r) })
	return r, h, store
}

func TestTus_OptionsReachableUnderProjectPath(t *testing.T) {
	r, _, _ := newTusStorageRouter(t)
	req := httptest.NewRequest("OPTIONS", "/api/projects/proj-s/storage/tus", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("OPTIONS on tus route: got %d body=%s", w.Code, w.Body.String())
	}
	if w.Header().Get("Tus-Version") == "" {
		t.Error("expected Tus-Version header from tusd handler")
	}
}

func TestTus_Disabled_WhenNotEnabled(t *testing.T) {
	// A handler without EnableResumableUploads must not expose /tus.
	store := newInMemoryBucketStoreForTest()
	r2, _ := storagesvc.NewR2Client(storagesvc.R2Config{
		AccessKeyID: "k", SecretAccessKey: "s",
		Endpoint: testR2URL, Bucket: testPlatformBucket,
	})
	h := NewStorageHandler(storagesvc.NewService(store, r2, nil), nil)
	r := chi.NewRouter()
	r.Route("/api/projects/{projectId}/storage", func(r chi.Router) { h.Routes(r) })

	req := httptest.NewRequest("OPTIONS", "/api/projects/proj-s/storage/tus", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Errorf("tus disabled should 404, got %d", w.Code)
	}
}

func TestTus_Create_RejectsMissingMetadata(t *testing.T) {
	r, _, _ := newTusStorageRouter(t)
	req := httptest.NewRequest("POST", "/api/projects/proj-s/storage/tus", nil)
	req.Header.Set("Tus-Resumable", "1.0.0")
	req.Header.Set("Upload-Length", "10")
	// no Upload-Metadata bucket/key
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code < 400 {
		t.Fatalf("create without bucket/key metadata should fail, got %d body=%s", w.Code, w.Body.String())
	}
}

func TestTus_Create_RejectsUnknownBucket(t *testing.T) {
	r, _, _ := newTusStorageRouter(t)
	req := httptest.NewRequest("POST", "/api/projects/proj-s/storage/tus", nil)
	req.Header.Set("Tus-Resumable", "1.0.0")
	req.Header.Set("Upload-Length", "10")
	// bucket=ghost (base64 "Z2hvc3Q="), key=a.txt (base64 "YS50eHQ=")
	req.Header.Set("Upload-Metadata", "bucket Z2hvc3Q=,key YS50eHQ=")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code < 400 {
		t.Fatalf("create to unknown bucket should fail, got %d body=%s", w.Code, w.Body.String())
	}
}

func TestTus_PrepareUpload_BuildsKeyAndKeepsMetadata(t *testing.T) {
	_, h, store := newTusStorageRouter(t)
	_ = store.CreateBucket(context.Background(), &storagesvc.Bucket{ID: "b1", ProjectID: "proj1", Name: "media"})

	ctx := context.WithValue(context.Background(), tusProjectCtxKey, "proj1")
	event := tusd.HookEvent{
		Context: ctx,
		Upload: tusd.FileInfo{
			Size:     123,
			MetaData: tusd.MetaData{"bucket": "media", "key": "clips/a.mp4", "filetype": "video/mp4"},
		},
	}
	_, changes, err := h.prepareTusUpload(event)
	if err != nil {
		t.Fatalf("prepareTusUpload: %v", err)
	}
	want := "projects/proj1/buckets/b1/clips/a.mp4"
	if changes.ID != want {
		t.Errorf("upload id = %q, want %q", changes.ID, want)
	}
	if changes.MetaData["bucket"] != "media" || changes.MetaData["key"] != "clips/a.mp4" {
		t.Errorf("metadata not retained: %+v", changes.MetaData)
	}
}

func TestTus_RecordUpload_WritesMetadata(t *testing.T) {
	_, h, store := newTusStorageRouter(t)
	_ = store.CreateBucket(context.Background(), &storagesvc.Bucket{ID: "b1", ProjectID: "proj1", Name: "media"})

	ctx := context.WithValue(context.Background(), tusProjectCtxKey, "proj1")
	event := tusd.HookEvent{
		Context: ctx,
		Upload: tusd.FileInfo{
			ID:       "projects/proj1/buckets/media/clips/a.mp4+mpid",
			Size:     4096,
			MetaData: tusd.MetaData{"bucket": "media", "key": "clips/a.mp4", "filetype": "video/mp4"},
		},
	}
	if _, err := h.recordTusUpload(event); err != nil {
		t.Fatalf("recordTusUpload: %v", err)
	}

	obj, err := store.GetObject(context.Background(), "b1", "clips/a.mp4")
	if err != nil {
		t.Fatalf("GetObject: %v", err)
	}
	if obj == nil {
		t.Fatal("expected object recorded after completion, got nil")
	}
	if obj.Size != 4096 || obj.MimeType != "video/mp4" {
		t.Errorf("recorded object mismatch: %+v", obj)
	}
}

func TestTus_LocationRewrite_IncludesMountPath(t *testing.T) {
	got := injectMountPath("https://api.example.com/abc123+mpid", "/api/projects/proj-s/storage/tus")
	want := "https://api.example.com/api/projects/proj-s/storage/tus/abc123+mpid"
	if got != want {
		t.Errorf("injectMountPath = %q, want %q", got, want)
	}
}
