package handler

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/edgefn"
	"github.com/excalibase/provisioning-poc/internal/k8s"
	"github.com/excalibase/provisioning-poc/internal/storage"
	"github.com/go-chi/chi/v5"
	"github.com/golang-jwt/jwt/v5"
)

const (
	testInvokePath       = "/invoke/"
	testDeletePath       = "/delete/"
	testAPIBase          = "https://api.test.io"
	testFunctionsRoute   = "/api/projects/{projectId}/functions"
	testIndexTS          = "index.ts"
	testProj1FnPath      = "/api/projects/proj_p1/functions/"
	testProj1HelloPath   = "/api/projects/proj_p1/functions/hello"
	testProj1SecretsPath = "/api/projects/proj_p1/functions/secrets"
	testDefaultHandler   = "export default () => new Response('ok')"
	testLazyProjNS       = "default-proj_lazy01"
	testDenoImage        = "excalibase/deno-runtime:test"
	testPublicInvokeRoute = "/functions/v1/{projectId}/{fnId}"
	testSecureFnPath     = "/functions/v1/proj_p1/secure"
)


// mockFnRuntime returns an httptest server that stands in for the Deno runtime
// with the new deploy/invoke protocol.
func mockFnRuntime(t *testing.T) (*httptest.Server, *map[string]edgefn.DeployRequest) {
	scripts := make(map[string]edgefn.DeployRequest)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set(sharedContentType, sharedMIMEJSON)
		switch {
		case r.URL.Path == "/health":
			json.NewEncoder(w).Encode(map[string]interface{}{"status": "healthy", "scripts": len(scripts)})
		case r.URL.Path == "/deploy" && r.Method == "POST":
			serveMockDeploy(w, r, scripts)
		case strings.HasPrefix(r.URL.Path, testInvokePath):
			serveMockInvoke(w, r.URL.Path, scripts)
		case strings.HasPrefix(r.URL.Path, testDeletePath):
			id := r.URL.Path[len(testDeletePath):]
			delete(scripts, id)
			w.WriteHeader(200)
		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &scripts
}

func serveMockDeploy(w http.ResponseWriter, r *http.Request, scripts map[string]edgefn.DeployRequest) {
	var body edgefn.DeployRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		w.WriteHeader(400)
		return
	}
	scripts[body.ID] = body
	w.WriteHeader(201)
	json.NewEncoder(w).Encode(map[string]string{"id": body.ID})
}

func serveMockInvoke(w http.ResponseWriter, path string, scripts map[string]edgefn.DeployRequest) {
	id := path[len(testInvokePath):]
	if _, ok := scripts[id]; !ok {
		w.WriteHeader(404)
		json.NewEncoder(w).Encode(map[string]string{"error": "not found"})
		return
	}
	json.NewEncoder(w).Encode(edgefn.InvokeResponse{
		Status:  200,
		Headers: map[string]string{sharedContentType: sharedMIMEJSON},
		Body:    `{"ok":true}`,
	})
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

	h := NewFunctionHandler(store, secrets, client, instStore, orgStore, testAPIBase)

	r := chi.NewRouter()
	r.Route(testFunctionsRoute, func(r chi.Router) {
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
func (f *fakeVault) Delete(p string) error { delete(f.data, p); return nil }
func (f *fakeVault) DeletePrefix(prefix string) (int, error) {
	if prefix == "" {
		return 0, errors.New("empty prefix")
	}
	n := 0
	for k := range f.data {
		if strings.HasPrefix(k, prefix) {
			delete(f.data, k)
			n++
		}
	}
	return n, nil
}
func (f *fakeVault) List(prefix string) ([]string, error) {
	out := []string{}
	for k := range f.data {
		if prefix == "" || strings.HasPrefix(k, prefix) {
			out = append(out, k)
		}
	}
	return out, nil
}
func (f *fakeVault) Sealed() bool                  { return false }
func (f *fakeVault) GetPublicKey() (string, error) { return "", nil }

// --- Tests ---

func doJSON(r chi.Router, method, path string, body interface{}) *httptest.ResponseRecorder {
	var buf bytes.Buffer
	if body != nil {
		json.NewEncoder(&buf).Encode(body)
	}
	req := httptest.NewRequest(method, path, &buf)
	req.Header.Set(sharedContentType, sharedMIMEJSON)
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
			{"path": testIndexTS, "content": "export default (req: Request) => new Response('hi')"},
		},
	}
	w := doJSON(r, "POST", testProj1FnPath, body)
	if w.Code != http.StatusCreated {
		t.Fatalf("create: got %d, body=%s", w.Code, w.Body.String())
	}

	// List — should see one
	w = doJSON(r, "GET", testProj1FnPath, nil)
	if w.Code != 200 {
		t.Fatalf("list: %d", w.Code)
	}
	var list []edgefn.Function
	json.Unmarshal(w.Body.Bytes(), &list)
	if len(list) != 1 || list[0].ID != "hello" {
		t.Errorf("list: %+v", list)
	}

	// Get
	w = doJSON(r, "GET", testProj1HelloPath, nil)
	if w.Code != 200 {
		t.Errorf("get: %d", w.Code)
	}

	// Delete
	w = doJSON(r, "DELETE", testProj1HelloPath, nil)
	if w.Code != 200 {
		t.Errorf("delete: %d", w.Code)
	}
	w = doJSON(r, "GET", testProj1HelloPath, nil)
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
			{"path": testIndexTS, "content": "export default (req: Request) => new Response('ok')"},
		},
	}
	doJSON(r, "POST", testProj1FnPath, body)

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
	w := doJSON(r, "POST", testProj1SecretsPath,
		map[string]string{"key": "STRIPE_KEY", "value": "sk_test_123"})
	if w.Code != 200 {
		t.Fatalf("set: %d, body=%s", w.Code, w.Body.String())
	}

	// Vault has it
	got, _ := v.Get("projects/proj_p1/edgefn/secrets")
	if got["STRIPE_KEY"] != "sk_test_123" {
		t.Errorf("vault: %+v", got)
	}

	// List
	w = doJSON(r, "GET", testProj1SecretsPath, nil)
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
	got, _ = v.Get("projects/proj_p1/edgefn/secrets")
	if _, ok := got["STRIPE_KEY"]; ok {
		t.Error("STRIPE_KEY should be gone")
	}
}

func TestFunctionHandler_Secrets_RejectsReservedKey(t *testing.T) {
	r, _, _, _ := setupFunctionHandler(t)
	w := doJSON(r, "POST", testProj1SecretsPath,
		map[string]string{"key": "EXCALIBASE_URL", "value": "evil"})
	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for reserved key, got %d", w.Code)
	}
}

// --- Built-in env injection (DB_URL, ANON_KEY, SERVICE_KEY) ---

func TestFunctionHandler_BuiltinEnv_WiresDBURLFromVault(t *testing.T) {
	store := edgefn.NewFunctionStore(t.TempDir())
	v := newFakeVault()
	// Seed app credentials the way createProjectRoles does
	v.data["projects/proj_p1/credentials/excalibase_app"] = map[string]string{
		"host":     "proj_p1-postgres-rw.default-proj_p1.svc.cluster.local",
		"port":     "5432",
		"database": "app",
		"username": "excalibase_app",
		"password": "p4ssw0rd",
	}
	secrets := edgefn.NewSecretsStore(v)
	runtime, scripts := mockFnRuntime(t)
	client := edgefn.NewRuntimeClient(runtime.URL, "")

	instStore := &inMemoryInstanceStore{insts: map[string]*domain.DatabaseInstance{
		"proj_p1": {ProjectID: "proj_p1", OrgID: "default"},
	}}
	h := NewFunctionHandler(store, secrets, client, instStore, nil, testAPIBase)
	h.SetVault(v)

	r := chi.NewRouter()
	r.Route(testFunctionsRoute, func(r chi.Router) {
		r.Post("/", h.Create)
	})

	body := map[string]interface{}{
		"id": "dbuser", "name": "DB User",
		"files": []map[string]string{
			{"path": testIndexTS, "content": "export default () => new Response(Deno.env.get('EXCALIBASE_DB_URL') || 'none')"},
		},
	}
	w := doJSON(r, "POST", testProj1FnPath, body)
	if w.Code != http.StatusCreated {
		t.Fatalf("create: %d body=%s", w.Code, w.Body.String())
	}

	// The mock runtime records every deploy. Verify the secrets payload
	// that was shipped actually contained the DB_URL.
	deploy, ok := (*scripts)["proj_p1__dbuser"]
	if !ok {
		t.Fatalf("deploy not recorded in mock runtime")
	}
	got := deploy.Secrets["EXCALIBASE_DB_URL"]
	want := "postgres://excalibase_app:p4ssw0rd@proj_p1-postgres-rw.default-proj_p1.svc.cluster.local:5432/app?sslmode=require"
	if got != want {
		t.Errorf("EXCALIBASE_DB_URL:\n got:  %q\n want: %q", got, want)
	}
	// Sanity — other builtins still there
	if deploy.Secrets["EXCALIBASE_PROJECT_ID"] != "proj_p1" {
		t.Errorf("PROJECT_ID missing: %+v", deploy.Secrets)
	}
}

func TestFunctionHandler_BuiltinEnv_WiresAnonAndServiceTokensFromVault(t *testing.T) {
	store := edgefn.NewFunctionStore(t.TempDir())
	v := newFakeVault()
	v.data["projects/proj_p1/credentials/jwt_keys/anon_token"] = map[string]string{
		"token": "eyJanon.token.here",
	}
	v.data["projects/proj_p1/credentials/jwt_keys/service_token"] = map[string]string{
		"token": "eyJservice.token.here",
	}
	secrets := edgefn.NewSecretsStore(v)
	runtime, scripts := mockFnRuntime(t)
	client := edgefn.NewRuntimeClient(runtime.URL, "")
	instStore := &inMemoryInstanceStore{insts: map[string]*domain.DatabaseInstance{
		"proj_p1": {ProjectID: "proj_p1", OrgID: "default"},
	}}
	h := NewFunctionHandler(store, secrets, client, instStore, nil, testAPIBase)
	h.SetVault(v)

	r := chi.NewRouter()
	r.Route(testFunctionsRoute, func(r chi.Router) { r.Post("/", h.Create) })

	body := map[string]interface{}{
		"id": "tokens", "name": "Tokens",
		"files": []map[string]string{
			{"path": testIndexTS, "content": testDefaultHandler},
		},
	}
	w := doJSON(r, "POST", testProj1FnPath, body)
	if w.Code != http.StatusCreated {
		t.Fatalf("create: %d body=%s", w.Code, w.Body.String())
	}

	deploy := (*scripts)["proj_p1__tokens"]
	if deploy.Secrets["EXCALIBASE_ANON_KEY"] != "eyJanon.token.here" {
		t.Errorf("anon: %q", deploy.Secrets["EXCALIBASE_ANON_KEY"])
	}
	if deploy.Secrets["EXCALIBASE_SERVICE_KEY"] != "eyJservice.token.here" {
		t.Errorf("service: %q", deploy.Secrets["EXCALIBASE_SERVICE_KEY"])
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
		"proj_lazy01": {ProjectID: "proj_lazy01", OrgID: "default", Namespace: testLazyProjNS},
	}}
	h := NewFunctionHandler(store, secrets, nil, instStore, nil, testAPIBase)
	h.SetK8sClient(mockK8s, testDenoImage, "secret")
	// Point lookups at the test server instead of cluster DNS
	h.SetRuntimeURLFn(func(_ string) string { return runtime.URL })

	r := chi.NewRouter()
	r.Route(testFunctionsRoute, func(r chi.Router) {
		r.Post("/", h.Create)
	})

	// Initially no runtime exists in the namespace
	if mockK8s.DenoRuntimes[testLazyProjNS] {
		t.Fatal("precondition: runtime should not exist")
	}

	// Deploy a function — handler should lazy-create the pod
	body := map[string]interface{}{
		"id": "hello", "name": "Hello",
		"files": []map[string]string{
			{"path": testIndexTS, "content": testDefaultHandler},
		},
	}
	w := doJSON(r, "POST", "/api/projects/proj_lazy01/functions/", body)
	if w.Code != http.StatusCreated {
		t.Fatalf("create: %d, body=%s", w.Code, w.Body.String())
	}

	// EnsureDenoRuntime should have been called for this namespace
	if !mockK8s.DenoRuntimes[testLazyProjNS] {
		t.Errorf("EnsureDenoRuntime was not called for default-proj_lazy01; calls=%v", mockK8s.Calls)
	}
}

func TestFunctionHandler_PerProject_PassesProjectTierToRuntimeSpec(t *testing.T) {
	store := edgefn.NewFunctionStore(t.TempDir())
	v := newFakeVault()
	secrets := edgefn.NewSecretsStore(v)
	runtime, _ := mockFnRuntime(t)

	mockK8s := k8s.NewMockClient()
	instStore := &inMemoryInstanceStore{insts: map[string]*domain.DatabaseInstance{
		"proj_free": {ProjectID: "proj_free", OrgID: "default", Namespace: "default-proj_free", Tier: domain.Free},
		"proj_std":  {ProjectID: "proj_std", OrgID: "default", Namespace: "default-proj_std", Tier: domain.Standard},
		"proj_entr": {ProjectID: "proj_entr", OrgID: "default", Namespace: "default-proj_entr", Tier: domain.Enterprise},
	}}
	h := NewFunctionHandler(store, secrets, nil, instStore, nil, "")
	h.SetK8sClient(mockK8s, testDenoImage, "secret")
	h.SetRuntimeURLFn(func(_ string) string { return runtime.URL })

	r := chi.NewRouter()
	r.Route(testFunctionsRoute, func(r chi.Router) { r.Post("/", h.Create) })

	for _, pid := range []string{"proj_free", "proj_std", "proj_entr"} {
		body := map[string]interface{}{
			"id": "fn", "name": "Fn",
			"files": []map[string]string{{"path": testIndexTS, "content": testDefaultHandler}},
		}
		w := doJSON(r, "POST", "/api/projects/"+pid+"/functions/", body)
		if w.Code != http.StatusCreated {
			t.Fatalf("create %s: %d body=%s", pid, w.Code, w.Body.String())
		}
	}

	// Mock tracks calls as "EnsureDenoRuntime:<ns>:<tier>" — verify each project
	// triggered a call with its own tier.
	hasCall := func(expected string) bool {
		for _, c := range mockK8s.Calls {
			if c == expected {
				return true
			}
		}
		return false
	}
	if !hasCall("EnsureDenoRuntime:default-proj_free:FREE") {
		t.Errorf("expected EnsureDenoRuntime with FREE tier for proj_free; calls=%v", mockK8s.Calls)
	}
	if !hasCall("EnsureDenoRuntime:default-proj_std:STANDARD") {
		t.Errorf("expected EnsureDenoRuntime with STANDARD tier for proj_std; calls=%v", mockK8s.Calls)
	}
	if !hasCall("EnsureDenoRuntime:default-proj_entr:ENTERPRISE") {
		t.Errorf("expected EnsureDenoRuntime with ENTERPRISE tier for proj_entr; calls=%v", mockK8s.Calls)
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
	h.SetK8sClient(mockK8s, testDenoImage, "secret")
	h.SetRuntimeURLFn(func(_ string) string { return runtime.URL })

	r := chi.NewRouter()
	r.Route(testFunctionsRoute, func(r chi.Router) {
		r.Post("/", h.Create)
	})

	// Create a function in each project
	for _, pid := range []string{"proj_a", "proj_b"} {
		body := map[string]interface{}{
			"id": "fn", "name": "Fn",
			"files": []map[string]string{
				{"path": testIndexTS, "content": testDefaultHandler},
			},
		}
		w := doJSON(r, "POST", "/api/projects/"+pid+"/functions/", body)
		if w.Code != http.StatusCreated {
			t.Fatalf("create %s: %d body=%s", pid, w.Code, w.Body.String())
		}
	}

	// Both namespaces should have ensure called exactly once each (tier suffix
	// is appended by the mock since the tier-aware spec change).
	ensureCalls := 0
	for _, c := range mockK8s.Calls {
		if strings.HasPrefix(c, "EnsureDenoRuntime:default-proj_a") ||
			strings.HasPrefix(c, "EnsureDenoRuntime:default-proj_b") {
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
	h := NewFunctionHandler(store, secrets, client, instStore, nil, testAPIBase)

	// Create with default verifyJwt=nil → defaults to true
	store.Save(&edgefn.Function{
		ProjectID: "proj_p1",
		ID:        "secured",
		Name:      "Secured",
		Files:     []edgefn.File{{Path: testIndexTS, Content: testDefaultHandler}},
		Active:    true,
	})
	// Pre-deploy in mock runtime so invoke would succeed if auth passed
	bundled, _ := (&edgefn.Function{
		ProjectID: "proj_p1", ID: "secured",
		Files: []edgefn.File{{Path: testIndexTS, Content: testDefaultHandler}},
	}).Bundle()
	client.Deploy(context.Background(), edgefn.DeployRequest{ID: "proj_p1__secured", Code: bundled})

	pub := chi.NewRouter()
	pub.HandleFunc(testPublicInvokeRoute, h.PublicInvoke)
	_ = r // unused

	// No Authorization header → 401
	req := httptest.NewRequest("POST", "/functions/v1/proj_p1/secured", nil)
	w := httptest.NewRecorder()
	pub.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 without auth header, got %d body=%s", w.Code, w.Body.String())
	}

	// With Authorization header but no signing key in vault → 503.
	// (Old behaviour was 200 via degraded-mode fallback; that was an authn
	// bypass — any caller could pass any string as a Bearer header.)
	req = httptest.NewRequest("POST", "/functions/v1/proj_p1/secured", nil)
	req.Header.Set("Authorization", "Bearer fake.jwt.token")
	w = httptest.NewRecorder()
	pub.ServeHTTP(w, req)
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503 when signing key unavailable, got %d body=%s", w.Code, w.Body.String())
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
	h := NewFunctionHandler(store, secrets, client, instStore, nil, testAPIBase)

	// Create with verifyJwt=false → public route allows unauth
	f := false
	store.Save(&edgefn.Function{
		ProjectID: "proj_p1",
		ID:        "webhook",
		Name:      "Webhook",
		VerifyJwt: &f,
		Files:     []edgefn.File{{Path: testIndexTS, Content: testDefaultHandler}},
		Active:    true,
	})
	client.Deploy(context.Background(), edgefn.DeployRequest{ID: "proj_p1__webhook", Code: "ok"})

	pub := chi.NewRouter()
	pub.HandleFunc(testPublicInvokeRoute, h.PublicInvoke)

	req := httptest.NewRequest("POST", "/functions/v1/proj_p1/webhook", nil)
	w := httptest.NewRecorder()
	pub.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Errorf("expected 200 (verifyJwt=false), got %d body=%s", w.Code, w.Body.String())
	}
}

func TestFunctionHandler_PublicInvoke_CORSPreflight(t *testing.T) {
	store := edgefn.NewFunctionStore(t.TempDir())
	v := newFakeVault()
	secrets := edgefn.NewSecretsStore(v)
	runtime, _ := mockFnRuntime(t)
	client := edgefn.NewRuntimeClient(runtime.URL, "")
	h := NewFunctionHandler(store, secrets, client, &inMemoryInstanceStore{insts: map[string]*domain.DatabaseInstance{
		"proj_p1": {ProjectID: "proj_p1", OrgID: "default"},
	}}, nil, "")

	pub := chi.NewRouter()
	pub.HandleFunc(testPublicInvokeRoute, h.PublicInvoke)

	req := httptest.NewRequest("OPTIONS", "/functions/v1/proj_p1/hello", nil)
	req.Header.Set("Origin", "https://app.example.com")
	req.Header.Set("Access-Control-Request-Method", "POST")
	w := httptest.NewRecorder()
	pub.ServeHTTP(w, req)

	if w.Code != http.StatusNoContent {
		t.Errorf("preflight: got %d, want 204", w.Code)
	}
	if got := w.Header().Get("Access-Control-Allow-Origin"); got == "" {
		t.Error("Access-Control-Allow-Origin not set")
	}
	if got := w.Header().Get("Access-Control-Allow-Methods"); got == "" {
		t.Error("Access-Control-Allow-Methods not set")
	}
	if got := w.Header().Get("Access-Control-Allow-Headers"); got == "" {
		t.Error("Access-Control-Allow-Headers not set")
	}
}

func TestFunctionHandler_PublicInvoke_CORSHeadersOnActualRequest(t *testing.T) {
	store := edgefn.NewFunctionStore(t.TempDir())
	v := newFakeVault()
	secrets := edgefn.NewSecretsStore(v)
	runtime, _ := mockFnRuntime(t)
	client := edgefn.NewRuntimeClient(runtime.URL, "")
	h := NewFunctionHandler(store, secrets, client, &inMemoryInstanceStore{insts: map[string]*domain.DatabaseInstance{
		"proj_p1": {ProjectID: "proj_p1", OrgID: "default"},
	}}, nil, "")

	f := false
	store.Save(&edgefn.Function{
		ProjectID: "proj_p1", ID: "hello", Name: "Hello", VerifyJwt: &f,
		Files:  []edgefn.File{{Path: testIndexTS, Content: testDefaultHandler}},
		Active: true,
	})
	client.Deploy(context.Background(), edgefn.DeployRequest{ID: "proj_p1__hello", Code: "ok"})

	pub := chi.NewRouter()
	pub.HandleFunc(testPublicInvokeRoute, h.PublicInvoke)

	req := httptest.NewRequest("POST", "/functions/v1/proj_p1/hello", nil)
	req.Header.Set("Origin", "https://app.example.com")
	w := httptest.NewRecorder()
	pub.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("status: %d body=%s", w.Code, w.Body.String())
	}
	if got := w.Header().Get("Access-Control-Allow-Origin"); got == "" {
		t.Error("Access-Control-Allow-Origin missing on actual response")
	}
}

func TestFunctionHandler_Invoke_ContentLengthCapRefusesOversized(t *testing.T) {
	r, _, _, _ := setupFunctionHandler(t)

	body := map[string]interface{}{
		"id":   "cap",
		"name": "Cap",
		"files": []map[string]string{
			{"path": testIndexTS, "content": testDefaultHandler},
		},
	}
	w := doJSON(r, "POST", testProj1FnPath, body)
	if w.Code != http.StatusCreated {
		t.Fatalf("setup deploy: %d body=%s", w.Code, w.Body.String())
	}

	// Invoke with an oversized Content-Length — should be rejected upfront
	// without the handler reading the body.
	req := httptest.NewRequest("POST", "/api/projects/proj_p1/functions/cap/invoke", bytes.NewReader([]byte("{}")))
	req.Header.Set(sharedContentType, sharedMIMEJSON)
	req.Header.Set("Content-Length", "100000000") // 100 MB
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("oversized Content-Length: got %d, want 413", rec.Code)
	}
}

// --- Real ES256 JWT verification ---

// setupVaultWithSigningKey creates a fakeVault seeded with an ES256 keypair
// at pki/signing/public. Returns the private key so tests can sign JWTs.
func setupVaultWithSigningKey(t *testing.T) (*fakeVault, *ecdsa.PrivateKey) {
	t.Helper()
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("gen key: %v", err)
	}
	pubDER, err := x509.MarshalPKIXPublicKey(&priv.PublicKey)
	if err != nil {
		t.Fatalf("marshal pub: %v", err)
	}
	pubPEM := pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: pubDER})
	v := newFakeVault()
	v.data["pki/signing/public"] = map[string]string{
		"key":       string(pubPEM),
		"algorithm": "EC-P256",
	}
	return v, priv
}

func signES256(t *testing.T, priv *ecdsa.PrivateKey, claims jwt.MapClaims) string {
	t.Helper()
	tok := jwt.NewWithClaims(jwt.SigningMethodES256, claims)
	signed, err := tok.SignedString(priv)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	return signed
}

func TestFunctionHandler_PublicInvoke_JWTValidSignatureAllowed(t *testing.T) {
	v, priv := setupVaultWithSigningKey(t)
	store := edgefn.NewFunctionStore(t.TempDir())
	secrets := edgefn.NewSecretsStore(v)
	runtime, _ := mockFnRuntime(t)
	client := edgefn.NewRuntimeClient(runtime.URL, "")
	h := NewFunctionHandler(store, secrets, client, &inMemoryInstanceStore{insts: map[string]*domain.DatabaseInstance{
		"proj_p1": {ProjectID: "proj_p1", OrgID: "default"},
	}}, nil, "")
	h.SetVault(v)
	// EXC-11: this case predates audience binding and covers signature
	// acceptance, so it opts out. TestFunctionHandler_PublicInvoke_Aud* cover
	// the audience requirement directly.
	h.SetAudienceRequirement(false, "")

	// verifyJwt default → nil means true
	store.Save(&edgefn.Function{
		ProjectID: "proj_p1", ID: "secure", Name: "Secure",
		Files:  []edgefn.File{{Path: testIndexTS, Content: testDefaultHandler}},
		Active: true,
	})
	client.Deploy(context.Background(), edgefn.DeployRequest{ID: "proj_p1__secure", Code: "ok"})

	validToken := signES256(t, priv, jwt.MapClaims{
		"iss":       "excalibase",
		"sub":       "user-alice",
		"projectId": "proj_p1",
		"exp":       time.Now().Add(time.Hour).Unix(),
	})

	pub := chi.NewRouter()
	pub.HandleFunc(testPublicInvokeRoute, h.PublicInvoke)

	req := httptest.NewRequest("POST", testSecureFnPath, nil)
	req.Header.Set("Authorization", sharedBearerPrefix+validToken)
	w := httptest.NewRecorder()
	pub.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Errorf("valid JWT should be accepted, got %d body=%s", w.Code, w.Body.String())
	}
}

func TestFunctionHandler_PublicInvoke_JWTTamperedSignatureRejected(t *testing.T) {
	v, priv := setupVaultWithSigningKey(t)
	store := edgefn.NewFunctionStore(t.TempDir())
	secrets := edgefn.NewSecretsStore(v)
	runtime, _ := mockFnRuntime(t)
	client := edgefn.NewRuntimeClient(runtime.URL, "")
	h := NewFunctionHandler(store, secrets, client, &inMemoryInstanceStore{insts: map[string]*domain.DatabaseInstance{
		"proj_p1": {ProjectID: "proj_p1", OrgID: "default"},
	}}, nil, "")
	h.SetVault(v)

	store.Save(&edgefn.Function{
		ProjectID: "proj_p1", ID: "secure", Name: "Secure",
		Files:  []edgefn.File{{Path: testIndexTS, Content: testDefaultHandler}},
		Active: true,
	})
	client.Deploy(context.Background(), edgefn.DeployRequest{ID: "proj_p1__secure", Code: "ok"})

	validToken := signES256(t, priv, jwt.MapClaims{"sub": "alice", "projectId": "proj_p1", "exp": time.Now().Add(time.Hour).Unix()})
	// Tamper signature deterministically: replace the LAST 5 chars of the
	// signature with "AAAAA". Signature is base64url, A is always a valid
	// base64 char so the token still parses but the bytes don't match.
	if len(validToken) < 5 {
		t.Fatalf("token too short to tamper: %s", validToken)
	}
	tampered := validToken[:len(validToken)-5] + "AAAAA"
	if tampered == validToken {
		// Cosmically unlikely — but safety net.
		tampered = validToken[:len(validToken)-5] + "BBBBB"
	}

	pub := chi.NewRouter()
	pub.HandleFunc(testPublicInvokeRoute, h.PublicInvoke)
	req := httptest.NewRequest("POST", testSecureFnPath, nil)
	req.Header.Set("Authorization", sharedBearerPrefix+tampered)
	w := httptest.NewRecorder()
	pub.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("tampered JWT should be 401, got %d", w.Code)
	}
}

func TestFunctionHandler_PublicInvoke_JWTExpiredRejected(t *testing.T) {
	v, priv := setupVaultWithSigningKey(t)
	store := edgefn.NewFunctionStore(t.TempDir())
	secrets := edgefn.NewSecretsStore(v)
	runtime, _ := mockFnRuntime(t)
	client := edgefn.NewRuntimeClient(runtime.URL, "")
	h := NewFunctionHandler(store, secrets, client, &inMemoryInstanceStore{insts: map[string]*domain.DatabaseInstance{
		"proj_p1": {ProjectID: "proj_p1", OrgID: "default"},
	}}, nil, "")
	h.SetVault(v)

	store.Save(&edgefn.Function{
		ProjectID: "proj_p1", ID: "secure", Name: "Secure",
		Files:  []edgefn.File{{Path: testIndexTS, Content: testDefaultHandler}},
		Active: true,
	})
	client.Deploy(context.Background(), edgefn.DeployRequest{ID: "proj_p1__secure", Code: "ok"})

	expired := signES256(t, priv, jwt.MapClaims{
		"sub":       "alice",
		"projectId": "proj_p1",
		"exp":       time.Now().Add(-time.Hour).Unix(), // expired 1h ago
	})

	pub := chi.NewRouter()
	pub.HandleFunc(testPublicInvokeRoute, h.PublicInvoke)
	req := httptest.NewRequest("POST", testSecureFnPath, nil)
	req.Header.Set("Authorization", sharedBearerPrefix+expired)
	w := httptest.NewRecorder()
	pub.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("expired JWT should be 401, got %d", w.Code)
	}
}

func TestFunctionHandler_PublicInvoke_JWTWrongProjectRejected(t *testing.T) {
	v, priv := setupVaultWithSigningKey(t)
	store := edgefn.NewFunctionStore(t.TempDir())
	secrets := edgefn.NewSecretsStore(v)
	runtime, _ := mockFnRuntime(t)
	client := edgefn.NewRuntimeClient(runtime.URL, "")
	h := NewFunctionHandler(store, secrets, client, &inMemoryInstanceStore{insts: map[string]*domain.DatabaseInstance{
		"proj_p1": {ProjectID: "proj_p1", OrgID: "default"},
		"proj_p2": {ProjectID: "proj_p2", OrgID: "default"},
	}}, nil, "")
	h.SetVault(v)

	store.Save(&edgefn.Function{
		ProjectID: "proj_p1", ID: "secure", Name: "Secure",
		Files:  []edgefn.File{{Path: testIndexTS, Content: testDefaultHandler}},
		Active: true,
	})
	client.Deploy(context.Background(), edgefn.DeployRequest{ID: "proj_p1__secure", Code: "ok"})

	// Token signed with valid key BUT projectId claim is proj_p2 — caller is
	// trying to replay a proj_p2 token against proj_p1's function.
	crossToken := signES256(t, priv, jwt.MapClaims{
		"iss":       "excalibase",
		"sub":       "alice",
		"projectId": "proj_p2",
		"exp":       time.Now().Add(time.Hour).Unix(),
	})

	pub := chi.NewRouter()
	pub.HandleFunc(testPublicInvokeRoute, h.PublicInvoke)
	req := httptest.NewRequest("POST", testSecureFnPath, nil)
	req.Header.Set("Authorization", sharedBearerPrefix+crossToken)
	w := httptest.NewRecorder()
	pub.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("cross-project JWT should be 401, got %d", w.Code)
	}
}

// A signed-but-claim-less JWT must be rejected: every legitimate token
// the auth service mints carries a projectId claim, so its absence is
// either a forged token or a downstream bug. Either way → 401.
func TestFunctionHandler_PublicInvoke_JWTMissingProjectIdRejected(t *testing.T) {
	v, priv := setupVaultWithSigningKey(t)
	store := edgefn.NewFunctionStore(t.TempDir())
	secrets := edgefn.NewSecretsStore(v)
	runtime, _ := mockFnRuntime(t)
	client := edgefn.NewRuntimeClient(runtime.URL, "")
	h := NewFunctionHandler(store, secrets, client, &inMemoryInstanceStore{insts: map[string]*domain.DatabaseInstance{
		"proj_p1": {ProjectID: "proj_p1", OrgID: "default"},
	}}, nil, "")
	h.SetVault(v)

	store.Save(&edgefn.Function{
		ProjectID: "proj_p1", ID: "secure", Name: "Secure",
		Files:  []edgefn.File{{Path: testIndexTS, Content: testDefaultHandler}},
		Active: true,
	})
	client.Deploy(context.Background(), edgefn.DeployRequest{ID: "proj_p1__secure", Code: "ok"})

	noProj := signES256(t, priv, jwt.MapClaims{
		"iss": "excalibase", "sub": "alice",
		"exp": time.Now().Add(time.Hour).Unix(),
		// Deliberately NO projectId claim.
	})

	pub := chi.NewRouter()
	pub.HandleFunc(testPublicInvokeRoute, h.PublicInvoke)
	req := httptest.NewRequest("POST", testSecureFnPath, nil)
	req.Header.Set("Authorization", sharedBearerPrefix+noProj)
	w := httptest.NewRecorder()
	pub.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("missing projectId claim should be 401, got %d body=%s", w.Code, w.Body.String())
	}
}

// When the operator configures EXCALIBASE_AUTH_ISS, tokens whose iss does
// not match are rejected even if signature + projectId are correct. This is
// defense-in-depth against a leaked signing key being used by a different
// issuer (tho with one platform-wide key that scenario is unlikely).
func TestFunctionHandler_PublicInvoke_JWTIssuerMismatchRejected(t *testing.T) {
	v, priv := setupVaultWithSigningKey(t)
	store := edgefn.NewFunctionStore(t.TempDir())
	secrets := edgefn.NewSecretsStore(v)
	runtime, _ := mockFnRuntime(t)
	client := edgefn.NewRuntimeClient(runtime.URL, "")
	h := NewFunctionHandler(store, secrets, client, &inMemoryInstanceStore{insts: map[string]*domain.DatabaseInstance{
		"proj_p1": {ProjectID: "proj_p1", OrgID: "default"},
	}}, nil, "")
	h.SetVault(v)
	h.SetExpectedJWTIssuer("excalibase")

	store.Save(&edgefn.Function{
		ProjectID: "proj_p1", ID: "secure", Name: "Secure",
		Files:  []edgefn.File{{Path: testIndexTS, Content: testDefaultHandler}},
		Active: true,
	})
	client.Deploy(context.Background(), edgefn.DeployRequest{ID: "proj_p1__secure", Code: "ok"})

	wrongIss := signES256(t, priv, jwt.MapClaims{
		"iss":       "rogue-issuer",
		"sub":       "alice",
		"projectId": "proj_p1",
		"exp":       time.Now().Add(time.Hour).Unix(),
	})

	pub := chi.NewRouter()
	pub.HandleFunc(testPublicInvokeRoute, h.PublicInvoke)
	req := httptest.NewRequest("POST", testSecureFnPath, nil)
	req.Header.Set("Authorization", sharedBearerPrefix+wrongIss)
	w := httptest.NewRecorder()
	pub.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("wrong iss should be 401 when expected issuer is configured, got %d", w.Code)
	}
}

// Scope claim from the JWT must reach the runtime via X-Excalibase-Scope so
// function code can branch on anon vs authenticated vs service. Verifies
// the header is forwarded.
func TestFunctionHandler_PublicInvoke_JWTScopeForwardedToRuntime(t *testing.T) {
	v, priv := setupVaultWithSigningKey(t)
	store := edgefn.NewFunctionStore(t.TempDir())
	secrets := edgefn.NewSecretsStore(v)

	// Custom mock runtime that records the inbound headers on /invoke.
	var gotScope string
	runtime := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, testInvokePath) {
			// Body is JSON { method, url, headers: {...}, body }
			var body struct {
				Headers map[string]string `json:"headers"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			gotScope = body.Headers["X-Excalibase-Scope"]
			w.Header().Set(sharedContentType, sharedMIMEJSON)
			w.Write([]byte(`{"status":200,"headers":{"content-type":"application/json"},"body":"ok"}`))
			return
		}
		if strings.HasPrefix(r.URL.Path, "/deploy") {
			w.Write([]byte(`{"id":"x","url":"x"}`))
			return
		}
		w.WriteHeader(200)
	}))
	t.Cleanup(runtime.Close)
	client := edgefn.NewRuntimeClient(runtime.URL, "")
	h := NewFunctionHandler(store, secrets, client, &inMemoryInstanceStore{insts: map[string]*domain.DatabaseInstance{
		"proj_p1": {ProjectID: "proj_p1", OrgID: "default"},
	}}, nil, "")
	h.SetVault(v)
	// EXC-11: this case is about scope forwarding, not audience binding.
	h.SetAudienceRequirement(false, "")

	store.Save(&edgefn.Function{
		ProjectID: "proj_p1", ID: "secure", Name: "Secure",
		Files:  []edgefn.File{{Path: testIndexTS, Content: testDefaultHandler}},
		Active: true,
	})
	client.Deploy(context.Background(), edgefn.DeployRequest{ID: "proj_p1__secure", Code: "ok"})

	tok := signES256(t, priv, jwt.MapClaims{
		"iss": "excalibase", "sub": "alice", "projectId": "proj_p1",
		"scope": "service",
		"exp":   time.Now().Add(time.Hour).Unix(),
	})

	pub := chi.NewRouter()
	pub.HandleFunc(testPublicInvokeRoute, h.PublicInvoke)
	req := httptest.NewRequest("POST", testSecureFnPath, nil)
	req.Header.Set("Authorization", sharedBearerPrefix+tok)
	w := httptest.NewRecorder()
	pub.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("valid JWT should pass, got %d", w.Code)
	}
	if gotScope != "service" {
		t.Errorf("X-Excalibase-Scope: got %q, want %q", gotScope, "service")
	}
}

func TestFunctionHandler_PublicInvoke_JWTAlgNoneDowngradeRejected(t *testing.T) {
	v, _ := setupVaultWithSigningKey(t)
	store := edgefn.NewFunctionStore(t.TempDir())
	secrets := edgefn.NewSecretsStore(v)
	runtime, _ := mockFnRuntime(t)
	client := edgefn.NewRuntimeClient(runtime.URL, "")
	h := NewFunctionHandler(store, secrets, client, &inMemoryInstanceStore{insts: map[string]*domain.DatabaseInstance{
		"proj_p1": {ProjectID: "proj_p1", OrgID: "default"},
	}}, nil, "")
	h.SetVault(v)

	store.Save(&edgefn.Function{
		ProjectID: "proj_p1", ID: "secure", Name: "Secure",
		Files:  []edgefn.File{{Path: testIndexTS, Content: testDefaultHandler}},
		Active: true,
	})
	client.Deploy(context.Background(), edgefn.DeployRequest{ID: "proj_p1__secure", Code: "ok"})

	// Unsigned token (alg=none). Attacker-facing header + payload + empty sig.
	noneToken := jwt.NewWithClaims(jwt.SigningMethodNone, jwt.MapClaims{
		"iss": "proj_p1", "sub": "attacker",
		"exp": time.Now().Add(time.Hour).Unix(),
	})
	tokStr, _ := noneToken.SignedString(jwt.UnsafeAllowNoneSignatureType)

	pub := chi.NewRouter()
	pub.HandleFunc(testPublicInvokeRoute, h.PublicInvoke)
	req := httptest.NewRequest("POST", testSecureFnPath, nil)
	req.Header.Set("Authorization", sharedBearerPrefix+tokStr)
	w := httptest.NewRecorder()
	pub.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("alg=none downgrade should be 401, got %d", w.Code)
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
	pub.HandleFunc(testPublicInvokeRoute, h.PublicInvoke)

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
		Files:     []edgefn.File{{Path: testIndexTS, Content: testDefaultHandler}},
		Active:    true,
	})
	client.Deploy(context.Background(), edgefn.DeployRequest{ID: "proj_p1__fast", Code: "ok"})

	pub := chi.NewRouter()
	pub.HandleFunc(testPublicInvokeRoute, h.PublicInvoke)

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
			Files:  []edgefn.File{{Path: testIndexTS, Content: testDefaultHandler}},
			Active: true,
		})
		client.Deploy(context.Background(), edgefn.DeployRequest{ID: pid + "__fn", Code: "ok"})
	}

	pub := chi.NewRouter()
	pub.HandleFunc(testPublicInvokeRoute, h.PublicInvoke)

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
			"files": []map[string]string{{"path": testIndexTS, "content": "export default () => new Response('x')"}},
		}
	}
	w := doJSON(r, "POST", testProj1FnPath, body("P1"))
	if w.Code != http.StatusCreated {
		t.Fatalf("create p1: %d", w.Code)
	}
	w = doJSON(r, "POST", "/api/projects/proj_p2/functions/", body("P2"))
	if w.Code != http.StatusCreated {
		t.Fatalf("create p2: %d", w.Code)
	}

	// Each project only sees its own hello
	w = doJSON(r, "GET", testProj1FnPath, nil)
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
