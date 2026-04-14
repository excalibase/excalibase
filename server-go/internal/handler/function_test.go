package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/edgefn"
	"github.com/excalibase/provisioning-poc/internal/k8s"
	"github.com/excalibase/provisioning-poc/internal/storage"
	"github.com/go-chi/chi/v5"
)

// mockFnRuntime returns an httptest server that stands in for the Deno runtime
// with the new deploy/invoke protocol.
func mockFnRuntime(t *testing.T) (*httptest.Server, *map[string]edgefn.DeployRequest) {
	scripts := make(map[string]edgefn.DeployRequest)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/health" {
			json.NewEncoder(w).Encode(map[string]interface{}{"status": "healthy", "scripts": len(scripts)})
			return
		}
		if r.URL.Path == "/deploy" && r.Method == "POST" {
			var body edgefn.DeployRequest
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				w.WriteHeader(400)
				return
			}
			scripts[body.ID] = body
			w.WriteHeader(201)
			json.NewEncoder(w).Encode(map[string]string{"id": body.ID})
			return
		}
		if len(r.URL.Path) > len("/invoke/") && r.URL.Path[:len("/invoke/")] == "/invoke/" {
			id := r.URL.Path[len("/invoke/"):]
			if _, ok := scripts[id]; !ok {
				w.WriteHeader(404)
				json.NewEncoder(w).Encode(map[string]string{"error": "not found"})
				return
			}
			json.NewEncoder(w).Encode(edgefn.InvokeResponse{
				Status:  200,
				Headers: map[string]string{"Content-Type": "application/json"},
				Body:    `{"ok":true}`,
			})
			return
		}
		if len(r.URL.Path) > len("/delete/") && r.URL.Path[:len("/delete/")] == "/delete/" {
			id := r.URL.Path[len("/delete/"):]
			delete(scripts, id)
			w.WriteHeader(200)
			return
		}
		w.WriteHeader(404)
	}))
	t.Cleanup(srv.Close)
	return srv, &scripts
}

// inMemoryInstanceStore provides the minimum InstanceStore surface needed by
// FunctionHandler.orgSlugFor.
type inMemoryInstanceStore struct {
	insts map[string]*domain.DatabaseInstance
}

func (s *inMemoryInstanceStore) Save(inst *domain.DatabaseInstance) error {
	s.insts[inst.ProjectID] = inst
	return nil
}
func (s *inMemoryInstanceStore) FindByProjectID(id string) (*domain.DatabaseInstance, error) {
	return s.insts[id], nil
}
func (s *inMemoryInstanceStore) FindAll() ([]*domain.DatabaseInstance, error) {
	out := make([]*domain.DatabaseInstance, 0, len(s.insts))
	for _, v := range s.insts {
		out = append(out, v)
	}
	return out, nil
}
func (s *inMemoryInstanceStore) FindByOwner(ownerID string) ([]*domain.DatabaseInstance, error) {
	out := make([]*domain.DatabaseInstance, 0)
	for _, v := range s.insts {
		if v.OwnerID == ownerID {
			out = append(out, v)
		}
	}
	return out, nil
}
func (s *inMemoryInstanceStore) Delete(id string) error { delete(s.insts, id); return nil }

func setupFunctionHandler(t *testing.T) (*chi.Mux, *edgefn.FunctionStore, *inMemoryInstanceStore, *fakeVault) {
	t.Helper()
	dir := t.TempDir()
	store := edgefn.NewFunctionStore(dir)

	v := newFakeVault()
	secrets := edgefn.NewSecretsStore(v)

	runtime, _ := mockFnRuntime(t)
	client := edgefn.NewRuntimeClient(runtime.URL, "")

	instStore := &inMemoryInstanceStore{insts: map[string]*domain.DatabaseInstance{
		"proj_p1": {ProjectID: "proj_p1", OrgID: "default"},
	}}
	var orgStore storage.OrgStore // nil is acceptable — handler falls back to "default"

	h := NewFunctionHandler(store, secrets, client, instStore, orgStore, "https://api.test.io")

	r := chi.NewRouter()
	r.Route("/api/projects/{projectId}/functions", func(r chi.Router) {
		r.Get("/", h.List)
		r.Post("/", h.Create)
		r.Get("/secrets", h.ListSecrets)
		r.Post("/secrets", h.SetSecret)
		r.Delete("/secrets/{key}", h.DeleteSecret)
		r.Route("/{fnId}", func(r chi.Router) {
			r.Get("/", h.Get)
			r.Delete("/", h.Delete)
			r.Post("/invoke", h.Invoke)
		})
	})
	return r, store, instStore, v
}

// fakeVault implements the same surface as the one in service/provisioning_test.go
// but local to this package to avoid cross-package test helper coupling.
type fakeVault struct {
	data map[string]map[string]string
}

func newFakeVault() *fakeVault { return &fakeVault{data: map[string]map[string]string{}} }

func (f *fakeVault) Get(p string) (map[string]string, error) {
	if d, ok := f.data[p]; ok {
		return d, nil
	}
	return nil, errors.New("not found")
}
func (f *fakeVault) Put(p string, d map[string]string) error {
	cp := make(map[string]string, len(d))
	for k, v := range d {
		cp[k] = v
	}
	f.data[p] = cp
	return nil
}
func (f *fakeVault) Delete(p string) error                { delete(f.data, p); return nil }
func (f *fakeVault) List(prefix string) ([]string, error) { return nil, nil }
func (f *fakeVault) Sealed() bool                         { return false }
func (f *fakeVault) GetPublicKey() (string, error)        { return "", nil }

// --- Tests ---

func doJSON(r chi.Router, method, path string, body interface{}) *httptest.ResponseRecorder {
	var buf bytes.Buffer
	if body != nil {
		json.NewEncoder(&buf).Encode(body)
	}
	req := httptest.NewRequest(method, path, &buf)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req.WithContext(context.Background()))
	return w
}

func TestFunctionHandler_CreateListGetDelete(t *testing.T) {
	r, _, _, _ := setupFunctionHandler(t)

	// Create
	body := map[string]interface{}{
		"id":   "hello",
		"name": "Hello",
		"files": []map[string]string{
			{"path": "index.ts", "content": "export default (req: Request) => new Response('hi')"},
		},
	}
	w := doJSON(r, "POST", "/api/projects/proj_p1/functions/", body)
	if w.Code != http.StatusCreated {
		t.Fatalf("create: got %d, body=%s", w.Code, w.Body.String())
	}

	// List — should see one
	w = doJSON(r, "GET", "/api/projects/proj_p1/functions/", nil)
	if w.Code != 200 {
		t.Fatalf("list: %d", w.Code)
	}
	var list []edgefn.Function
	json.Unmarshal(w.Body.Bytes(), &list)
	if len(list) != 1 || list[0].ID != "hello" {
		t.Errorf("list: %+v", list)
	}

	// Get
	w = doJSON(r, "GET", "/api/projects/proj_p1/functions/hello", nil)
	if w.Code != 200 {
		t.Errorf("get: %d", w.Code)
	}

	// Delete
	w = doJSON(r, "DELETE", "/api/projects/proj_p1/functions/hello", nil)
	if w.Code != 200 {
		t.Errorf("delete: %d", w.Code)
	}
	w = doJSON(r, "GET", "/api/projects/proj_p1/functions/hello", nil)
	if w.Code != 404 {
		t.Errorf("get after delete: %d, want 404", w.Code)
	}
}

func TestFunctionHandler_Invoke(t *testing.T) {
	r, _, _, _ := setupFunctionHandler(t)

	// Deploy
	body := map[string]interface{}{
		"id":   "echo",
		"name": "Echo",
		"files": []map[string]string{
			{"path": "index.ts", "content": "export default (req: Request) => new Response('ok')"},
		},
	}
	doJSON(r, "POST", "/api/projects/proj_p1/functions/", body)

	// Invoke
	w := doJSON(r, "POST", "/api/projects/proj_p1/functions/echo/invoke", map[string]string{"name": "world"})
	if w.Code != 200 {
		t.Fatalf("invoke: %d, body=%s", w.Code, w.Body.String())
	}
	if w.Body.String() != `{"ok":true}` {
		t.Errorf("body: %q", w.Body.String())
	}
}

func TestFunctionHandler_Secrets_SetListDelete(t *testing.T) {
	r, _, _, v := setupFunctionHandler(t)

	// Set
	w := doJSON(r, "POST", "/api/projects/proj_p1/functions/secrets",
		map[string]string{"key": "STRIPE_KEY", "value": "sk_test_123"})
	if w.Code != 200 {
		t.Fatalf("set: %d, body=%s", w.Code, w.Body.String())
	}

	// Vault has it
	got, _ := v.Get("projects/default/proj_p1/edgefn/secrets")
	if got["STRIPE_KEY"] != "sk_test_123" {
		t.Errorf("vault: %+v", got)
	}

	// List
	w = doJSON(r, "GET", "/api/projects/proj_p1/functions/secrets", nil)
	if w.Code != 200 {
		t.Fatalf("list secrets: %d", w.Code)
	}
	var keys []map[string]string
	json.Unmarshal(w.Body.Bytes(), &keys)
	if len(keys) != 1 || keys[0]["key"] != "STRIPE_KEY" {
		t.Errorf("keys: %+v", keys)
	}
	// Values never exposed
	if w.Body.Len() < 1 || bytes_Contains(w.Body.Bytes(), "sk_test_123") {
		t.Errorf("secret value must not appear in list response: %s", w.Body.String())
	}

	// Delete
	w = doJSON(r, "DELETE", "/api/projects/proj_p1/functions/secrets/STRIPE_KEY", nil)
	if w.Code != 200 {
		t.Errorf("delete: %d", w.Code)
	}
	got, _ = v.Get("projects/default/proj_p1/edgefn/secrets")
	if _, ok := got["STRIPE_KEY"]; ok {
		t.Error("STRIPE_KEY should be gone")
	}
}

func TestFunctionHandler_Secrets_RejectsReservedKey(t *testing.T) {
	r, _, _, _ := setupFunctionHandler(t)
	w := doJSON(r, "POST", "/api/projects/proj_p1/functions/secrets",
		map[string]string{"key": "EXCALIBASE_URL", "value": "evil"})
	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for reserved key, got %d", w.Code)
	}
}

// --- Per-project pod lazy provisioning ---

func TestFunctionHandler_PerProject_LazyDeploysRuntime(t *testing.T) {
	store := edgefn.NewFunctionStore(t.TempDir())
	v := newFakeVault()
	secrets := edgefn.NewSecretsStore(v)
	runtime, _ := mockFnRuntime(t)

	mockK8s := k8s.NewMockClient()
	instStore := &inMemoryInstanceStore{insts: map[string]*domain.DatabaseInstance{
		"proj_lazy01": {ProjectID: "proj_lazy01", OrgID: "default", Namespace: "default-proj_lazy01"},
	}}
	h := NewFunctionHandler(store, secrets, nil, instStore, nil, "https://api.test.io")
	h.SetK8sClient(mockK8s, "excalibase/deno-runtime:test", "secret")
	// Point lookups at the test server instead of cluster DNS
	h.SetRuntimeURLFn(func(_ string) string { return runtime.URL })

	r := chi.NewRouter()
	r.Route("/api/projects/{projectId}/functions", func(r chi.Router) {
		r.Post("/", h.Create)
	})

	// Initially no runtime exists in the namespace
	if mockK8s.DenoRuntimes["default-proj_lazy01"] {
		t.Fatal("precondition: runtime should not exist")
	}

	// Deploy a function — handler should lazy-create the pod
	body := map[string]interface{}{
		"id": "hello", "name": "Hello",
		"files": []map[string]string{
			{"path": "index.ts", "content": "export default () => new Response('ok')"},
		},
	}
	w := doJSON(r, "POST", "/api/projects/proj_lazy01/functions/", body)
	if w.Code != http.StatusCreated {
		t.Fatalf("create: %d, body=%s", w.Code, w.Body.String())
	}

	// EnsureDenoRuntime should have been called for this namespace
	if !mockK8s.DenoRuntimes["default-proj_lazy01"] {
		t.Errorf("EnsureDenoRuntime was not called for default-proj_lazy01; calls=%v", mockK8s.Calls)
	}
}

func TestFunctionHandler_PerProject_RuntimeClientCachedPerProject(t *testing.T) {
	store := edgefn.NewFunctionStore(t.TempDir())
	v := newFakeVault()
	secrets := edgefn.NewSecretsStore(v)
	runtime, _ := mockFnRuntime(t)

	mockK8s := k8s.NewMockClient()
	instStore := &inMemoryInstanceStore{insts: map[string]*domain.DatabaseInstance{
		"proj_a": {ProjectID: "proj_a", OrgID: "default", Namespace: "default-proj_a"},
		"proj_b": {ProjectID: "proj_b", OrgID: "default", Namespace: "default-proj_b"},
	}}
	h := NewFunctionHandler(store, secrets, nil, instStore, nil, "")
	h.SetK8sClient(mockK8s, "excalibase/deno-runtime:test", "secret")
	h.SetRuntimeURLFn(func(_ string) string { return runtime.URL })

	r := chi.NewRouter()
	r.Route("/api/projects/{projectId}/functions", func(r chi.Router) {
		r.Post("/", h.Create)
	})

	// Create a function in each project
	for _, pid := range []string{"proj_a", "proj_b"} {
		body := map[string]interface{}{
			"id": "fn", "name": "Fn",
			"files": []map[string]string{
				{"path": "index.ts", "content": "export default () => new Response('ok')"},
			},
		}
		w := doJSON(r, "POST", "/api/projects/"+pid+"/functions/", body)
		if w.Code != http.StatusCreated {
			t.Fatalf("create %s: %d body=%s", pid, w.Code, w.Body.String())
		}
	}

	// Both namespaces should have ensure called exactly once each
	ensureCalls := 0
	for _, c := range mockK8s.Calls {
		if c == "EnsureDenoRuntime:default-proj_a" || c == "EnsureDenoRuntime:default-proj_b" {
			ensureCalls++
		}
	}
	if ensureCalls != 2 {
		t.Errorf("expected 2 EnsureDenoRuntime calls (one per project), got %d. calls=%v", ensureCalls, mockK8s.Calls)
	}
}

// --- Public invoke + verifyJwt + rate limit ---

func TestFunctionHandler_PublicInvoke_VerifyJwtRequiresAuthHeader(t *testing.T) {
	r, _, _, _ := setupFunctionHandler(t)

	// Add the public route (mirrors what main.go wires)
	store := edgefn.NewFunctionStore(t.TempDir())
	v := newFakeVault()
	secrets := edgefn.NewSecretsStore(v)
	runtime, _ := mockFnRuntime(t)
	client := edgefn.NewRuntimeClient(runtime.URL, "")
	instStore := &inMemoryInstanceStore{insts: map[string]*domain.DatabaseInstance{
		"proj_p1": {ProjectID: "proj_p1", OrgID: "default"},
	}}
	h := NewFunctionHandler(store, secrets, client, instStore, nil, "https://api.test.io")

	// Create with default verifyJwt=nil → defaults to true
	store.Save(&edgefn.Function{
		ProjectID: "proj_p1",
		ID:        "secured",
		Name:      "Secured",
		Files:     []edgefn.File{{Path: "index.ts", Content: "export default () => new Response('ok')"}},
		Active:    true,
	})
	// Pre-deploy in mock runtime so invoke would succeed if auth passed
	bundled, _ := (&edgefn.Function{
		ProjectID: "proj_p1", ID: "secured",
		Files: []edgefn.File{{Path: "index.ts", Content: "export default () => new Response('ok')"}},
	}).Bundle()
	client.Deploy(context.Background(), edgefn.DeployRequest{ID: "proj_p1__secured", Code: bundled})

	pub := chi.NewRouter()
	pub.HandleFunc("/functions/v1/{projectId}/{fnId}", h.PublicInvoke)
	_ = r // unused

	// No Authorization header → 401
	req := httptest.NewRequest("POST", "/functions/v1/proj_p1/secured", nil)
	w := httptest.NewRecorder()
	pub.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 without auth header, got %d body=%s", w.Code, w.Body.String())
	}

	// With Authorization header → 200 (mock runtime returns ok)
	req = httptest.NewRequest("POST", "/functions/v1/proj_p1/secured", nil)
	req.Header.Set("Authorization", "Bearer fake.jwt.token")
	w = httptest.NewRecorder()
	pub.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Errorf("expected 200 with auth header, got %d body=%s", w.Code, w.Body.String())
	}
}

func TestFunctionHandler_PublicInvoke_VerifyJwtFalseAllowsUnauth(t *testing.T) {
	store := edgefn.NewFunctionStore(t.TempDir())
	v := newFakeVault()
	secrets := edgefn.NewSecretsStore(v)
	runtime, _ := mockFnRuntime(t)
	client := edgefn.NewRuntimeClient(runtime.URL, "")
	instStore := &inMemoryInstanceStore{insts: map[string]*domain.DatabaseInstance{
		"proj_p1": {ProjectID: "proj_p1", OrgID: "default"},
	}}
	h := NewFunctionHandler(store, secrets, client, instStore, nil, "https://api.test.io")

	// Create with verifyJwt=false → public route allows unauth
	f := false
	store.Save(&edgefn.Function{
		ProjectID: "proj_p1",
		ID:        "webhook",
		Name:      "Webhook",
		VerifyJwt: &f,
		Files:     []edgefn.File{{Path: "index.ts", Content: "export default () => new Response('ok')"}},
		Active:    true,
	})
	client.Deploy(context.Background(), edgefn.DeployRequest{ID: "proj_p1__webhook", Code: "ok"})

	pub := chi.NewRouter()
	pub.HandleFunc("/functions/v1/{projectId}/{fnId}", h.PublicInvoke)

	req := httptest.NewRequest("POST", "/functions/v1/proj_p1/webhook", nil)
	w := httptest.NewRecorder()
	pub.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Errorf("expected 200 (verifyJwt=false), got %d body=%s", w.Code, w.Body.String())
	}
}

func TestFunctionHandler_PublicInvoke_NotFoundReturns404(t *testing.T) {
	store := edgefn.NewFunctionStore(t.TempDir())
	v := newFakeVault()
	secrets := edgefn.NewSecretsStore(v)
	runtime, _ := mockFnRuntime(t)
	client := edgefn.NewRuntimeClient(runtime.URL, "")
	h := NewFunctionHandler(store, secrets, client, &inMemoryInstanceStore{insts: map[string]*domain.DatabaseInstance{}}, nil, "")

	pub := chi.NewRouter()
	pub.HandleFunc("/functions/v1/{projectId}/{fnId}", h.PublicInvoke)

	req := httptest.NewRequest("POST", "/functions/v1/proj_nope/missing", nil)
	req.Header.Set("Authorization", "Bearer x")
	w := httptest.NewRecorder()
	pub.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404 for missing function, got %d", w.Code)
	}
}

func TestFunctionHandler_RateLimit_BlocksAfterBudget(t *testing.T) {
	store := edgefn.NewFunctionStore(t.TempDir())
	v := newFakeVault()
	secrets := edgefn.NewSecretsStore(v)
	runtime, _ := mockFnRuntime(t)
	client := edgefn.NewRuntimeClient(runtime.URL, "")
	instStore := &inMemoryInstanceStore{insts: map[string]*domain.DatabaseInstance{
		"proj_p1": {ProjectID: "proj_p1", OrgID: "default"},
	}}
	h := NewFunctionHandler(store, secrets, client, instStore, nil, "")
	// Tight budget for the test: 3 requests, then deny
	h.SetRateLimit(3, 3)

	f := false
	store.Save(&edgefn.Function{
		ProjectID: "proj_p1", ID: "fast", Name: "Fast",
		VerifyJwt: &f,
		Files:     []edgefn.File{{Path: "index.ts", Content: "export default () => new Response('ok')"}},
		Active:    true,
	})
	client.Deploy(context.Background(), edgefn.DeployRequest{ID: "proj_p1__fast", Code: "ok"})

	pub := chi.NewRouter()
	pub.HandleFunc("/functions/v1/{projectId}/{fnId}", h.PublicInvoke)

	hit := func() int {
		req := httptest.NewRequest("POST", "/functions/v1/proj_p1/fast", nil)
		w := httptest.NewRecorder()
		pub.ServeHTTP(w, req)
		return w.Code
	}

	// First 3 succeed
	for i := 0; i < 3; i++ {
		if got := hit(); got != 200 {
			t.Errorf("req %d: %d, want 200", i, got)
		}
	}
	// 4th is rate limited
	if got := hit(); got != http.StatusTooManyRequests {
		t.Errorf("4th req: %d, want 429", got)
	}
}

func TestFunctionHandler_RateLimit_PerProjectIsolated(t *testing.T) {
	store := edgefn.NewFunctionStore(t.TempDir())
	v := newFakeVault()
	secrets := edgefn.NewSecretsStore(v)
	runtime, _ := mockFnRuntime(t)
	client := edgefn.NewRuntimeClient(runtime.URL, "")
	instStore := &inMemoryInstanceStore{insts: map[string]*domain.DatabaseInstance{
		"proj_p1": {ProjectID: "proj_p1", OrgID: "default"},
		"proj_p2": {ProjectID: "proj_p2", OrgID: "default"},
	}}
	h := NewFunctionHandler(store, secrets, client, instStore, nil, "")
	h.SetRateLimit(2, 2)

	f := false
	for _, pid := range []string{"proj_p1", "proj_p2"} {
		store.Save(&edgefn.Function{
			ProjectID: pid, ID: "fn", Name: "Fn", VerifyJwt: &f,
			Files:  []edgefn.File{{Path: "index.ts", Content: "export default () => new Response('ok')"}},
			Active: true,
		})
		client.Deploy(context.Background(), edgefn.DeployRequest{ID: pid + "__fn", Code: "ok"})
	}

	pub := chi.NewRouter()
	pub.HandleFunc("/functions/v1/{projectId}/{fnId}", h.PublicInvoke)

	hit := func(pid string) int {
		req := httptest.NewRequest("POST", "/functions/v1/"+pid+"/fn", nil)
		w := httptest.NewRecorder()
		pub.ServeHTTP(w, req)
		return w.Code
	}

	// Burn through proj_p1's budget
	hit("proj_p1")
	hit("proj_p1")
	if got := hit("proj_p1"); got != http.StatusTooManyRequests {
		t.Errorf("p1 third: %d, want 429", got)
	}
	// proj_p2 still has its own budget
	if got := hit("proj_p2"); got != 200 {
		t.Errorf("p2 first (after p1 limited): %d, want 200", got)
	}
}

func TestFunctionHandler_ProjectScopingPreventsCollision(t *testing.T) {
	r, _, instStore, _ := setupFunctionHandler(t)
	instStore.insts["proj_p2"] = &domain.DatabaseInstance{ProjectID: "proj_p2", OrgID: "default"}

	// Same function id in two projects
	body := func(name string) map[string]interface{} {
		return map[string]interface{}{
			"id": "hello", "name": name,
			"files": []map[string]string{{"path": "index.ts", "content": "export default () => new Response('x')"}},
		}
	}
	w := doJSON(r, "POST", "/api/projects/proj_p1/functions/", body("P1"))
	if w.Code != http.StatusCreated {
		t.Fatalf("create p1: %d", w.Code)
	}
	w = doJSON(r, "POST", "/api/projects/proj_p2/functions/", body("P2"))
	if w.Code != http.StatusCreated {
		t.Fatalf("create p2: %d", w.Code)
	}

	// Each project only sees its own hello
	w = doJSON(r, "GET", "/api/projects/proj_p1/functions/", nil)
	var list1 []edgefn.Function
	json.Unmarshal(w.Body.Bytes(), &list1)
	if len(list1) != 1 || list1[0].Name != "P1" {
		t.Errorf("p1 list: %+v", list1)
	}

	w = doJSON(r, "GET", "/api/projects/proj_p2/functions/", nil)
	var list2 []edgefn.Function
	json.Unmarshal(w.Body.Bytes(), &list2)
	if len(list2) != 1 || list2[0].Name != "P2" {
		t.Errorf("p2 list: %+v", list2)
	}
}

func bytes_Contains(haystack []byte, needle string) bool {
	return bytes.Contains(haystack, []byte(needle))
}
