package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/edgefn"
	"github.com/excalibase/provisioning-poc/internal/storagesvc"
	"github.com/go-chi/chi/v5"
)

// Phase 10 — internal storage routes used by `ctx.storage` in the Deno
// runtime. Mirrors Phase 7's `/internal/invoke` pattern: server-to-server,
// authenticated via the runtime-token shared secret (not user JWT). The
// runtime never sees user credentials, so it can't ride the regular
// `/api/projects/.../storage` routes that require RequireAuth +
// RequireProjectAccess.
//
// Routes under test:
//   POST   /internal/storage/{projectId}/upload-url     mint a signed PUT URL
//   POST   /internal/storage/{projectId}/download-url   mint a signed GET URL
//   GET    /internal/storage/{projectId}/metadata/{id}  metadata for a stored id
//   DELETE /internal/storage/{projectId}/{id}           remove a stored id
//
// All four use a single fixed bucket per project (`_ctx_storage`) that the
// handler auto-provisions on first use. The bucket is private — ctx.storage
// returns signed URLs only.

const (
	testStorageProjectID = "proj_storage_t"
)

// Per-project runtime token the internal storage routes now expect (SEC-C5).
var testStorageRuntimeToken = edgefn.DeriveRuntimeSecret("the-secret", testStorageProjectID)

// inMemoryBucketStoreForTest is a minimal BucketStore that lets the tests
// exercise the handler without touching the SQL implementations. Mirrors
// the structure of storagesvc.memStore in service_test.go but lives in the
// handler package so it can be shared across internal storage tests.
type inMemoryBucketStoreForTest struct {
	mu      sync.Mutex
	buckets map[string]*storagesvc.Bucket
	objects map[string]map[string]*storagesvc.Object
	quotas  map[string]int64
}

func newInMemoryBucketStoreForTest() *inMemoryBucketStoreForTest {
	return &inMemoryBucketStoreForTest{
		buckets: map[string]*storagesvc.Bucket{},
		objects: map[string]map[string]*storagesvc.Object{},
		quotas:  map[string]int64{},
	}
}

func (m *inMemoryBucketStoreForTest) CreateBucket(_ context.Context, b *storagesvc.Bucket) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, existing := range m.buckets {
		if existing.ProjectID == b.ProjectID && existing.Name == b.Name {
			return errStorageTestAlreadyExists
		}
	}
	m.buckets[b.ID] = b
	m.objects[b.ID] = map[string]*storagesvc.Object{}
	return nil
}

func (m *inMemoryBucketStoreForTest) GetBucket(_ context.Context, projectID, name string) (*storagesvc.Bucket, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, b := range m.buckets {
		if b.ProjectID == projectID && b.Name == name {
			return b, nil
		}
	}
	return nil, nil
}

func (m *inMemoryBucketStoreForTest) ListBuckets(_ context.Context, projectID string) ([]storagesvc.Bucket, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []storagesvc.Bucket{}
	for _, b := range m.buckets {
		if b.ProjectID == projectID {
			out = append(out, *b)
		}
	}
	return out, nil
}

func (m *inMemoryBucketStoreForTest) ListAllBuckets(_ context.Context) ([]storagesvc.Bucket, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []storagesvc.Bucket{}
	for _, b := range m.buckets {
		out = append(out, *b)
	}
	return out, nil
}

func (m *inMemoryBucketStoreForTest) DeleteBucket(_ context.Context, projectID, name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for id, b := range m.buckets {
		if b.ProjectID == projectID && b.Name == name {
			delete(m.buckets, id)
			delete(m.objects, id)
			return nil
		}
	}
	return errStorageTestNotFound
}

func (m *inMemoryBucketStoreForTest) SetBucketStatus(_ context.Context, projectID, name, status string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, b := range m.buckets {
		if b.ProjectID == projectID && b.Name == name {
			b.Status = status
			return nil
		}
	}
	return errStorageTestNotFound
}

func (m *inMemoryBucketStoreForTest) CreateObject(_ context.Context, o *storagesvc.Object) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.objects[o.BucketID]; !ok {
		m.objects[o.BucketID] = map[string]*storagesvc.Object{}
	}
	m.objects[o.BucketID][o.Key] = o
	return nil
}

func (m *inMemoryBucketStoreForTest) GetObject(_ context.Context, bucketID, key string) (*storagesvc.Object, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if b, ok := m.objects[bucketID]; ok {
		return b[key], nil
	}
	return nil, nil
}

func (m *inMemoryBucketStoreForTest) ListObjects(_ context.Context, bucketID, prefix string, limit int, _ string) ([]storagesvc.Object, string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []storagesvc.Object{}
	for _, o := range m.objects[bucketID] {
		if prefix == "" || strings.HasPrefix(o.Key, prefix) {
			out = append(out, *o)
			if limit > 0 && len(out) >= limit {
				break
			}
		}
	}
	return out, "", nil
}

func (m *inMemoryBucketStoreForTest) DeleteObjectAndReleaseQuota(_ context.Context, projectID, bucketID, key string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	obj, ok := m.objects[bucketID][key]
	if !ok {
		return false, nil
	}
	delete(m.objects[bucketID], key)
	m.quotas[projectID] -= obj.Size
	if m.quotas[projectID] < 0 {
		m.quotas[projectID] = 0
	}
	return true, nil
}

func (m *inMemoryBucketStoreForTest) GetQuotaBytes(_ context.Context, projectID string) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.quotas[projectID], nil
}

func (m *inMemoryBucketStoreForTest) AddQuotaBytes(_ context.Context, projectID string, delta int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.quotas[projectID] += delta
	return nil
}

var errStorageTestAlreadyExists = stringErrorForStorageTest("already exists")
var errStorageTestNotFound = stringErrorForStorageTest("not found")

type stringErrorForStorageTest string

func (e stringErrorForStorageTest) Error() string { return string(e) }

// newStorageInternalRouter builds a chi router with the storage handler's
// internal routes mounted at /internal/storage. Returns the router plus
// the bucket store so tests can inspect persistence directly.
func newStorageInternalRouter(t *testing.T, runtimeSecret string) (chi.Router, *inMemoryBucketStoreForTest) {
	r, store, _ := newStorageInternalRouterWithBackend(t, runtimeSecret)
	return r, store
}

// newStorageInternalRouterWithBackend also hands back the blob plane, so a
// test can say what the object store really holds before confirming.
func newStorageInternalRouterWithBackend(t *testing.T, runtimeSecret string) (chi.Router, *inMemoryBucketStoreForTest, *storageBackendStub) {
	t.Helper()
	store := newInMemoryBucketStoreForTest()
	backend := newStorageBackendStub()
	h := NewStorageHandler(newStubbedStorageService(t, store, backend, nil), nil)
	h.SetRuntimeSecret(runtimeSecret)

	r := chi.NewRouter()
	h.InternalRoutes(r)
	return r, store, backend
}

const (
	testR2URL          = "https://acct.r2.cloudflarestorage.com"
	testPlatformBucket = "excalibase-test"
)

// TestInternalStorage_UploadURL_RejectsMissingRuntimeToken — the route MUST
// require X-Excalibase-Runtime-Token. Anything else is a 401 with no body
// leak about what method/path was hit (parity with Phase 7 /internal/invoke).
func TestInternalStorage_UploadURL_RejectsMissingRuntimeToken(t *testing.T) {
	r, _ := newStorageInternalRouter(t, "the-secret")
	req := httptest.NewRequest("POST",
		"/internal/storage/"+testStorageProjectID+"/upload-url",
		bytes.NewReader([]byte(`{"contentType":"image/png","size":16}`)))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("missing runtime token: want 401, got %d (body=%s)", w.Code, w.Body.String())
	}
}

// TestInternalStorage_UploadURL_RejectsWrongRuntimeToken — same check, but
// with the wrong value. The handler must use constant-time comparison; we
// don't try to assert timing here, just that the wrong value fails closed.
func TestInternalStorage_UploadURL_RejectsWrongRuntimeToken(t *testing.T) {
	r, _ := newStorageInternalRouter(t, "the-secret")
	req := httptest.NewRequest("POST",
		"/internal/storage/"+testStorageProjectID+"/upload-url",
		bytes.NewReader([]byte(`{"contentType":"image/png","size":16}`)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(runtimeTokenHeader, "wrong-secret")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("wrong runtime token: want 401, got %d (body=%s)", w.Code, w.Body.String())
	}
}

// TestInternalStorage_UploadURL_AutoProvisionsBucket — the route mints a
// signed PUT URL on first call AND auto-creates the conventional
// `_ctx_storage` bucket. Subsequent calls reuse the same bucket.
func TestInternalStorage_UploadURL_AutoProvisionsBucket(t *testing.T) {
	r, store := newStorageInternalRouter(t, "the-secret")
	body := []byte(`{"contentType":"image/png","size":16}`)
	req := httptest.NewRequest("POST",
		"/internal/storage/"+testStorageProjectID+"/upload-url",
		bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(runtimeTokenHeader, testStorageRuntimeToken)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("upload-url: want 200, got %d (body=%s)", w.Code, w.Body.String())
	}
	var resp struct {
		StorageID string `json:"storageId"`
		URL       string `json:"url"`
		Method    string `json:"method"`
	}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.StorageID == "" {
		t.Errorf("storageId missing in response")
	}
	if resp.Method != "PUT" {
		t.Errorf("method: want PUT, got %q", resp.Method)
	}
	if !strings.Contains(resp.URL, resp.StorageID) {
		t.Errorf("signed URL should address the minted storage id: %q", resp.URL)
	}
	if !strings.Contains(resp.URL, "content-length") {
		t.Errorf("signed PUT must bind the content length: %q", resp.URL)
	}
	// Bucket auto-created.
	b, err := store.GetBucket(context.Background(), testStorageProjectID, ctxStorageBucket)
	if err != nil {
		t.Fatalf("get bucket: %v", err)
	}
	if b == nil {
		t.Errorf("bucket %q should have been auto-created", ctxStorageBucket)
	}
	// Second call reuses the same bucket (no duplicate-create error).
	w2 := httptest.NewRecorder()
	req2 := httptest.NewRequest("POST",
		"/internal/storage/"+testStorageProjectID+"/upload-url",
		bytes.NewReader(body))
	req2.Header.Set("Content-Type", "application/json")
	req2.Header.Set(runtimeTokenHeader, testStorageRuntimeToken)
	r.ServeHTTP(w2, req2)
	if w2.Code != http.StatusOK {
		t.Errorf("second upload-url: want 200, got %d (body=%s)", w2.Code, w2.Body.String())
	}
}

// TestInternalStorage_DownloadURL_RoundTrip — confirm-upload first (the
// client PUTs to the signed URL elsewhere, then the runtime records the
// upload via /confirm-upload), then download-url returns a signed GET URL
// for that storage id.
func TestInternalStorage_DownloadURL_RoundTrip(t *testing.T) {
	r, _, backend := newStorageInternalRouterWithBackend(t, "the-secret")

	// 1. upload-url → get storageId
	mint := mustPost(t, r, "/internal/storage/"+testStorageProjectID+"/upload-url",
		`{"contentType":"text/plain","size":4}`)
	var minted struct{ StorageID string `json:"storageId"` }
	if err := json.Unmarshal(mint, &minted); err != nil {
		t.Fatalf("mint decode: %v", err)
	}
	if minted.StorageID == "" {
		t.Fatalf("no storage id minted")
	}
	backend.put(minted.StorageID, 4, "text/plain")

	// 2. confirm-upload (records the metadata row)
	confirmBody := `{"storageId":"` + minted.StorageID + `","contentType":"text/plain","size":4,"sha256":"deadbeef"}`
	mustPost(t, r, "/internal/storage/"+testStorageProjectID+"/confirm-upload", confirmBody)

	// 3. download-url → returns signed URL
	dlBody := `{"storageId":"` + minted.StorageID + `"}`
	dl := mustPost(t, r, "/internal/storage/"+testStorageProjectID+"/download-url", dlBody)
	var dlResp struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal(dl, &dlResp); err != nil {
		t.Fatalf("dl decode: %v", err)
	}
	if !strings.Contains(dlResp.URL, minted.StorageID) {
		t.Errorf("download URL should address the storage id, got %q", dlResp.URL)
	}
}

// TestInternalStorage_DownloadURL_ReturnsNullOnMissing — a request for a
// storage id that doesn't exist yet (no confirm-upload) must surface as
// 404 (StorageReader.getUrl returns null when null is encoded as 404).
func TestInternalStorage_DownloadURL_ReturnsNullOnMissing(t *testing.T) {
	r, _ := newStorageInternalRouter(t, "the-secret")
	req := httptest.NewRequest("POST",
		"/internal/storage/"+testStorageProjectID+"/download-url",
		bytes.NewReader([]byte(`{"storageId":"kg2_unknown"}`)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(runtimeTokenHeader, testStorageRuntimeToken)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Errorf("missing id: want 404, got %d (body=%s)", w.Code, w.Body.String())
	}
}

// TestInternalStorage_Metadata_ReturnsRow — once an object is recorded
// via confirm-upload, GET /metadata/{storageId} returns the metadata row.
func TestInternalStorage_Metadata_ReturnsRow(t *testing.T) {
	r, _, backend := newStorageInternalRouterWithBackend(t, "the-secret")
	mint := mustPost(t, r, "/internal/storage/"+testStorageProjectID+"/upload-url",
		`{"contentType":"image/jpeg","size":1024}`)
	var minted struct{ StorageID string `json:"storageId"` }
	_ = json.Unmarshal(mint, &minted)
	backend.put(minted.StorageID, 1024, "image/jpeg")

	confirmBody := `{"storageId":"` + minted.StorageID + `","contentType":"image/jpeg","size":1024,"sha256":"abc123"}`
	mustPost(t, r, "/internal/storage/"+testStorageProjectID+"/confirm-upload", confirmBody)

	req := httptest.NewRequest("GET",
		"/internal/storage/"+testStorageProjectID+"/metadata/"+minted.StorageID, nil)
	req.Header.Set(runtimeTokenHeader, testStorageRuntimeToken)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("metadata: want 200, got %d (body=%s)", w.Code, w.Body.String())
	}
	var meta struct {
		StorageID   string `json:"storageId"`
		Size        int64  `json:"size"`
		Sha256      string `json:"sha256"`
		ContentType string `json:"contentType"`
	}
	if err := json.NewDecoder(w.Body).Decode(&meta); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if meta.StorageID != minted.StorageID {
		t.Errorf("storageId mismatch: want %q, got %q", minted.StorageID, meta.StorageID)
	}
	if meta.Size != 1024 {
		t.Errorf("size: want 1024, got %d", meta.Size)
	}
	if meta.Sha256 != "abc123" {
		t.Errorf("sha256: want abc123, got %q", meta.Sha256)
	}
	if meta.ContentType != "image/jpeg" {
		t.Errorf("contentType: want image/jpeg, got %q", meta.ContentType)
	}
}

// TestInternalStorage_Metadata_ReturnsNullOnMissing — non-existent id 404s
// (the runtime translates 404 into `null` for StorageReader.getMetadata).
func TestInternalStorage_Metadata_ReturnsNullOnMissing(t *testing.T) {
	r, _ := newStorageInternalRouter(t, "the-secret")
	req := httptest.NewRequest("GET",
		"/internal/storage/"+testStorageProjectID+"/metadata/kg2_unknown", nil)
	req.Header.Set(runtimeTokenHeader, testStorageRuntimeToken)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Errorf("missing id: want 404, got %d", w.Code)
	}
}

// TestInternalStorage_Delete_RemovesObject — delete is idempotent: a second
// DELETE on the same id returns 204 again.
func TestInternalStorage_Delete_RemovesObject(t *testing.T) {
	r, _, backend := newStorageInternalRouterWithBackend(t, "the-secret")
	mint := mustPost(t, r, "/internal/storage/"+testStorageProjectID+"/upload-url",
		`{"contentType":"text/plain","size":4}`)
	var minted struct{ StorageID string `json:"storageId"` }
	_ = json.Unmarshal(mint, &minted)
	backend.put(minted.StorageID, 4, "text/plain")
	mustPost(t, r, "/internal/storage/"+testStorageProjectID+"/confirm-upload",
		`{"storageId":"`+minted.StorageID+`","contentType":"text/plain","size":4,"sha256":"d"}`)

	// First delete.
	req := httptest.NewRequest("DELETE",
		"/internal/storage/"+testStorageProjectID+"/"+minted.StorageID, nil)
	req.Header.Set(runtimeTokenHeader, testStorageRuntimeToken)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusNoContent {
		t.Errorf("delete: want 204, got %d (body=%s)", w.Code, w.Body.String())
	}

	// Metadata should now 404.
	mreq := httptest.NewRequest("GET",
		"/internal/storage/"+testStorageProjectID+"/metadata/"+minted.StorageID, nil)
	mreq.Header.Set(runtimeTokenHeader, testStorageRuntimeToken)
	mw := httptest.NewRecorder()
	r.ServeHTTP(mw, mreq)
	if mw.Code != http.StatusNotFound {
		t.Errorf("post-delete metadata: want 404, got %d", mw.Code)
	}

	// Second delete: idempotent — still 204.
	req2 := httptest.NewRequest("DELETE",
		"/internal/storage/"+testStorageProjectID+"/"+minted.StorageID, nil)
	req2.Header.Set(runtimeTokenHeader, testStorageRuntimeToken)
	w2 := httptest.NewRecorder()
	r.ServeHTTP(w2, req2)
	if w2.Code != http.StatusNoContent {
		t.Errorf("second delete: want 204, got %d (body=%s)", w2.Code, w2.Body.String())
	}
}

// TestInternalStorage_Delete_RejectsMissingRuntimeToken — delete must also
// be auth-gated (regression safeguard so a future refactor doesn't drop
// the auth check on this verb).
func TestInternalStorage_Delete_RejectsMissingRuntimeToken(t *testing.T) {
	r, _ := newStorageInternalRouter(t, "the-secret")
	req := httptest.NewRequest("DELETE",
		"/internal/storage/"+testStorageProjectID+"/kg2_x", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("missing token: want 401, got %d", w.Code)
	}
}

// TestInternalStorage_RejectsInvalidProjectID — runtime never makes up
// project ids, but a buggy caller could. Same ProjectID validation as the
// Phase 7 internal-invoke route.
func TestInternalStorage_RejectsInvalidProjectID(t *testing.T) {
	r, _ := newStorageInternalRouter(t, "the-secret")
	req := httptest.NewRequest("POST",
		"/internal/storage/bad..proj/upload-url",
		bytes.NewReader([]byte(`{}`)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(runtimeTokenHeader, testStorageRuntimeToken)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("invalid project id: want 400, got %d (body=%s)", w.Code, w.Body.String())
	}
}

// TestInternalStorage_ConfirmUpload_RejectsInvalidBody — malformed JSON
// body must surface as 400 (defensive parse).
func TestInternalStorage_ConfirmUpload_RejectsInvalidBody(t *testing.T) {
	r, _ := newStorageInternalRouter(t, "the-secret")
	req := httptest.NewRequest("POST",
		"/internal/storage/"+testStorageProjectID+"/confirm-upload",
		bytes.NewReader([]byte("not-json")))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(runtimeTokenHeader, testStorageRuntimeToken)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("malformed body: want 400, got %d", w.Code)
	}
}

// TestInternalStorage_ConfirmUpload_RejectsEmptyStorageID — server must
// require a non-empty storageId since downstream calls (delete, metadata)
// key off it.
func TestInternalStorage_ConfirmUpload_RejectsEmptyStorageID(t *testing.T) {
	r, _ := newStorageInternalRouter(t, "the-secret")
	req := httptest.NewRequest("POST",
		"/internal/storage/"+testStorageProjectID+"/confirm-upload",
		bytes.NewReader([]byte(`{"storageId":"","size":1,"contentType":"text/plain"}`)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(runtimeTokenHeader, testStorageRuntimeToken)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("empty storageId: want 400, got %d", w.Code)
	}
}

// TestInternalStorage_DownloadURL_RejectsEmptyStorageID — same guard on
// the download-url path.
func TestInternalStorage_DownloadURL_RejectsEmptyStorageID(t *testing.T) {
	r, _ := newStorageInternalRouter(t, "the-secret")
	req := httptest.NewRequest("POST",
		"/internal/storage/"+testStorageProjectID+"/download-url",
		bytes.NewReader([]byte(`{"storageId":""}`)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(runtimeTokenHeader, testStorageRuntimeToken)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("empty storageId: want 400, got %d", w.Code)
	}
}

// TestInternalStorage_DownloadURL_RejectsInvalidBody — malformed JSON
// body 400s on download-url path too.
func TestInternalStorage_DownloadURL_RejectsInvalidBody(t *testing.T) {
	r, _ := newStorageInternalRouter(t, "the-secret")
	req := httptest.NewRequest("POST",
		"/internal/storage/"+testStorageProjectID+"/download-url",
		bytes.NewReader([]byte("not-json")))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(runtimeTokenHeader, testStorageRuntimeToken)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("malformed body: want 400, got %d", w.Code)
	}
}

// TestInternalStorage_RejectsInvalidProjectIDOnAllRoutes — same project-id
// validation must guard every internal route, not just upload-url. Hit
// each one to lock the gate in place.
func TestInternalStorage_RejectsInvalidProjectIDOnAllRoutes(t *testing.T) {
	r, _ := newStorageInternalRouter(t, "the-secret")
	type call struct {
		method string
		path   string
		body   string
	}
	cases := []call{
		{"POST", "/internal/storage/bad..p/upload-url", `{}`},
		{"POST", "/internal/storage/bad..p/confirm-upload", `{"storageId":"kg2_a","size":1}`},
		{"POST", "/internal/storage/bad..p/download-url", `{"storageId":"kg2_a"}`},
		{"GET", "/internal/storage/bad..p/metadata/kg2_a", ""},
		{"DELETE", "/internal/storage/bad..p/kg2_a", ""},
	}
	for _, c := range cases {
		var body *strings.Reader
		if c.body != "" {
			body = strings.NewReader(c.body)
		}
		var req *http.Request
		if body != nil {
			req = httptest.NewRequest(c.method, c.path, body)
		} else {
			req = httptest.NewRequest(c.method, c.path, nil)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set(runtimeTokenHeader, testStorageRuntimeToken)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != http.StatusBadRequest {
			t.Errorf("%s %s: want 400, got %d", c.method, c.path, w.Code)
		}
	}
}

// TestInternalStorage_RouteAuthCheckCoverage — every internal route must
// reject a missing runtime token. The first three tests already cover a
// subset; this one fans out across the remaining verbs to keep the auth
// gate honest as the route table grows.
func TestInternalStorage_RouteAuthCheckCoverage(t *testing.T) {
	r, _ := newStorageInternalRouter(t, "the-secret")
	type call struct {
		method string
		path   string
		body   string
	}
	cases := []call{
		{"POST", "/internal/storage/" + testStorageProjectID + "/confirm-upload", `{"storageId":"kg2_a","size":1}`},
		{"POST", "/internal/storage/" + testStorageProjectID + "/download-url", `{"storageId":"kg2_a"}`},
		{"GET", "/internal/storage/" + testStorageProjectID + "/metadata/kg2_a", ""},
	}
	for _, c := range cases {
		var body *strings.Reader
		if c.body != "" {
			body = strings.NewReader(c.body)
		}
		var req *http.Request
		if body != nil {
			req = httptest.NewRequest(c.method, c.path, body)
		} else {
			req = httptest.NewRequest(c.method, c.path, nil)
		}
		// Intentionally NO runtime token header.
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != http.StatusUnauthorized {
			t.Errorf("%s %s without token: want 401, got %d", c.method, c.path, w.Code)
		}
	}
}

// TestInternalStorage_EmptySecretDisablesRoutes — passing the runtime
// secret as "" must lock down every internal route (no caller can present
// an empty header value and pass).
func TestInternalStorage_EmptySecretDisablesRoutes(t *testing.T) {
	r, _ := newStorageInternalRouter(t, "")
	req := httptest.NewRequest("POST",
		"/internal/storage/"+testStorageProjectID+"/upload-url",
		bytes.NewReader([]byte(`{}`)))
	req.Header.Set("Content-Type", "application/json")
	// Token "" — handler should refuse regardless.
	req.Header.Set(runtimeTokenHeader, "")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("empty secret: want 401, got %d", w.Code)
	}
}

// mustPost is a tiny test helper: POST against the test router with the
// runtime-token header, decode 2xx response body as raw bytes, t.Fatalf on
// non-success.
func mustPost(t *testing.T, r chi.Router, path, body string) []byte {
	t.Helper()
	req := httptest.NewRequest("POST", path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(runtimeTokenHeader, testStorageRuntimeToken)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code < 200 || w.Code >= 300 {
		t.Fatalf("%s %s: status %d (body=%s)", req.Method, path, w.Code, w.Body.String())
	}
	return w.Body.Bytes()
}

// TestInternalStorage_CrossProjectTokenRejected pins SEC-C5: a runtime token
// minted for one project must NOT authenticate an internal storage call for a
// different project. Before the per-project derivation, every deno pod shared
// one secret, so project A's runtime could sign project B's storage URLs.
func TestInternalStorage_CrossProjectTokenRejected(t *testing.T) {
	r, _ := newStorageInternalRouter(t, "the-secret")
	// Valid token, but for a DIFFERENT project than the path targets.
	foreignToken := edgefn.DeriveRuntimeSecret("the-secret", "proj_other_tenant")
	req := httptest.NewRequest("POST",
		"/internal/storage/"+testStorageProjectID+"/upload-url",
		strings.NewReader(`{"contentType":"text/plain","size":10}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(runtimeTokenHeader, foreignToken)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("cross-project runtime token must be rejected (SEC-C5): got %d (body=%s)", w.Code, w.Body.String())
	}
}
