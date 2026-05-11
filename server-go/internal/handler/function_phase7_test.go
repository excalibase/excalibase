package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/edgefn"
	"github.com/go-chi/chi/v5"
)

const (
	testInternalOnlyFnID    = "admin_only"
	testInternalInvokeRoute = "/internal/invoke/{projectId}/{fnId}"
	testHTTPActionFnID      = "webhook"
	testHTTPRouterFnID      = "api_http"
)

// --- Phase 7: PublicInvoke must 404 for internal-only functions ---

// TestPublicInvoke_404OnInternalFunction confirms that a function persisted
// with IsInternal=true returns 404 on the public route, indistinguishable
// from a missing function. The deny goes ahead of any other check (no
// rate-limit accounting, no JWT validation, no runtime forwarding) so an
// attacker can't enumerate internal fnIDs via timing or side-channels.
func TestPublicInvoke_404OnInternalFunction(t *testing.T) {
	store := edgefn.NewFunctionStore(t.TempDir())
	v := newFakeVault()
	secrets := edgefn.NewSecretsStore(v)
	runtime, _ := mockFnRuntime(t)
	client := edgefn.NewRuntimeClient(runtime.URL, "")
	instStore := &inMemoryInstanceStore{insts: map[string]*domain.DatabaseInstance{
		"proj_p1": {ProjectID: "proj_p1", OrgID: "default"},
	}}
	h := NewFunctionHandler(store, secrets, client, instStore, nil, testAPIBase)

	// Function persisted with IsInternal=true and VerifyJwt=false so the only
	// thing the test exercises is the internal gate.
	verify := false
	if err := store.Save(&edgefn.Function{
		ProjectID:  "proj_p1",
		ID:         testInternalOnlyFnID,
		Name:       "Internal Only",
		Files:      []edgefn.File{{Path: testIndexTS, Content: testDefaultHandler}},
		Active:     true,
		IsInternal: true,
		VerifyJwt:  &verify,
	}); err != nil {
		t.Fatalf("save: %v", err)
	}

	pub := chi.NewRouter()
	pub.HandleFunc(testPublicInvokeRoute, h.PublicInvoke)
	req := httptest.NewRequest("POST", "/functions/v1/proj_p1/"+testInternalOnlyFnID, nil)
	w := httptest.NewRecorder()
	pub.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Errorf("internal function via PublicInvoke: got %d, want 404 (body=%s)", w.Code, w.Body.String())
	}
	// Response body MUST mirror the regular not-found shape so callers can't
	// tell "internal-only" apart from "missing".
	if !bytes.Contains(w.Body.Bytes(), []byte(errFunctionNotFound)) {
		t.Errorf("body should reuse not-found phrasing for parity, got %s", w.Body.String())
	}
}

// --- Phase 7: InternalInvoke route (server-to-server bridge) ---

// TestInternalInvoke_RouteAuthCheck exercises the new
// POST /internal/invoke/{projectId}/{fnId} route used by the Deno runtime
// when ctx.runQuery/runMutation/runAction targets a function in a different
// worker. Authenticated with the runtime-token shared secret. Anything else
// (no header, wrong header) must 401.
func TestInternalInvoke_RouteAuthCheck(t *testing.T) {
	store := edgefn.NewFunctionStore(t.TempDir())
	v := newFakeVault()
	secrets := edgefn.NewSecretsStore(v)
	runtime, _ := mockFnRuntime(t)
	client := edgefn.NewRuntimeClient(runtime.URL, "test-runtime-secret")
	instStore := &inMemoryInstanceStore{insts: map[string]*domain.DatabaseInstance{
		"proj_p1": {ProjectID: "proj_p1", OrgID: "default"},
	}}
	h := NewFunctionHandler(store, secrets, client, instStore, nil, testAPIBase)
	h.SetK8sClient(nil, "", "test-runtime-secret")

	// Internal-only target function. Internal-invoke allows it; PublicInvoke
	// would 404.
	if err := store.Save(&edgefn.Function{
		ProjectID:  "proj_p1",
		ID:         testInternalOnlyFnID,
		Name:       "Internal",
		Files:      []edgefn.File{{Path: testIndexTS, Content: testDefaultHandler}},
		Active:     true,
		IsInternal: true,
	}); err != nil {
		t.Fatalf("save: %v", err)
	}
	// Pre-deploy in the mock runtime so the forward succeeds.
	bundled, _ := (&edgefn.Function{
		ProjectID: "proj_p1", ID: testInternalOnlyFnID,
		Files: []edgefn.File{{Path: testIndexTS, Content: testDefaultHandler}},
	}).Bundle()
	client.Deploy(context.Background(), edgefn.DeployRequest{
		ID: "proj_p1__" + testInternalOnlyFnID, Code: bundled,
	})

	rtr := chi.NewRouter()
	rtr.Post(testInternalInvokeRoute, h.InternalInvoke)

	// No header → 401
	req := httptest.NewRequest("POST", "/internal/invoke/proj_p1/"+testInternalOnlyFnID,
		bytes.NewBufferString(`{"args":{}}`))
	w := httptest.NewRecorder()
	rtr.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("no runtime token: got %d, want 401", w.Code)
	}

	// Wrong header → 401
	req = httptest.NewRequest("POST", "/internal/invoke/proj_p1/"+testInternalOnlyFnID,
		bytes.NewBufferString(`{"args":{}}`))
	req.Header.Set("X-Excalibase-Runtime-Token", "wrong-secret")
	w = httptest.NewRecorder()
	rtr.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("wrong runtime token: got %d, want 401", w.Code)
	}

	// Correct header → forwards through to runtime → 200
	req = httptest.NewRequest("POST", "/internal/invoke/proj_p1/"+testInternalOnlyFnID,
		bytes.NewBufferString(`{"args":{}}`))
	req.Header.Set("X-Excalibase-Runtime-Token", "test-runtime-secret")
	w = httptest.NewRecorder()
	rtr.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("authenticated internal invoke: got %d body=%s", w.Code, w.Body.String())
	}
}

// --- Phase 7: HTTP action / router dispatch routes ---

// TestPublicHttpRoute_DispatchesToHttpActionFunction confirms that
// /functions/v1/{projectId}/http/* lands on the project's httpAction-kind
// function (or matches against an httpRouter-kind function's route table)
// and forwards the raw request to the runtime.
func TestPublicHttpRoute_DispatchesToHttpActionFunction(t *testing.T) {
	store := edgefn.NewFunctionStore(t.TempDir())
	v := newFakeVault()
	secrets := edgefn.NewSecretsStore(v)
	runtime, _ := mockFnRuntime(t)
	client := edgefn.NewRuntimeClient(runtime.URL, "")
	instStore := &inMemoryInstanceStore{insts: map[string]*domain.DatabaseInstance{
		"proj_p1": {ProjectID: "proj_p1", OrgID: "default"},
	}}
	h := NewFunctionHandler(store, secrets, client, instStore, nil, testAPIBase)

	verify := false
	if err := store.Save(&edgefn.Function{
		ProjectID:  "proj_p1",
		ID:         testHTTPActionFnID,
		Name:       "Webhook",
		Files:      []edgefn.File{{Path: testIndexTS, Content: testDefaultHandler}},
		Active:     true,
		Kind:       "httpAction",
		VerifyJwt:  &verify,
	}); err != nil {
		t.Fatalf("save: %v", err)
	}
	client.Deploy(context.Background(), edgefn.DeployRequest{
		ID: "proj_p1__" + testHTTPActionFnID, Code: testDefaultHandler,
	})

	rtr := chi.NewRouter()
	rtr.HandleFunc("/functions/v1/{projectId}/http/*", h.PublicHttpInvoke)

	req := httptest.NewRequest("POST", "/functions/v1/proj_p1/http/webhook",
		bytes.NewBufferString("raw body"))
	w := httptest.NewRecorder()
	rtr.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("httpAction dispatch: got %d body=%s", w.Code, w.Body.String())
	}
}

// TestPublicHttpRoute_MatchesRouterRoute confirms that an httpRouter-kind
// function's persisted route table drives path matching: a request to
// /functions/v1/proj/http/status matches the { path: "/status", method: "GET" }
// row in HttpRoutes and forwards to the runtime.
func TestPublicHttpRoute_MatchesRouterRoute(t *testing.T) {
	store := edgefn.NewFunctionStore(t.TempDir())
	v := newFakeVault()
	secrets := edgefn.NewSecretsStore(v)
	runtime, _ := mockFnRuntime(t)
	client := edgefn.NewRuntimeClient(runtime.URL, "")
	instStore := &inMemoryInstanceStore{insts: map[string]*domain.DatabaseInstance{
		"proj_p1": {ProjectID: "proj_p1", OrgID: "default"},
	}}
	h := NewFunctionHandler(store, secrets, client, instStore, nil, testAPIBase)

	verify := false
	routes := json.RawMessage(`[
	  {"path":"/status","method":"GET","exportName":"default"},
	  {"path":"/webhook","method":"POST","exportName":"default"}
	]`)
	if err := store.Save(&edgefn.Function{
		ProjectID:  "proj_p1",
		ID:         testHTTPRouterFnID,
		Name:       "Api Http",
		Files:      []edgefn.File{{Path: testIndexTS, Content: testDefaultHandler}},
		Active:     true,
		Kind:       "httpRouter",
		HttpRoutes: routes,
		VerifyJwt:  &verify,
	}); err != nil {
		t.Fatalf("save: %v", err)
	}
	client.Deploy(context.Background(), edgefn.DeployRequest{
		ID: "proj_p1__" + testHTTPRouterFnID, Code: testDefaultHandler,
	})

	rtr := chi.NewRouter()
	rtr.HandleFunc("/functions/v1/{projectId}/http/*", h.PublicHttpInvoke)

	// /status with GET matches the first route row
	req := httptest.NewRequest("GET", "/functions/v1/proj_p1/http/status", nil)
	w := httptest.NewRecorder()
	rtr.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("router GET /status: got %d body=%s", w.Code, w.Body.String())
	}

	// /webhook with POST matches the second route row
	req = httptest.NewRequest("POST", "/functions/v1/proj_p1/http/webhook",
		bytes.NewBufferString("payload"))
	w = httptest.NewRecorder()
	rtr.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("router POST /webhook: got %d body=%s", w.Code, w.Body.String())
	}

	// /unknown 404
	req = httptest.NewRequest("GET", "/functions/v1/proj_p1/http/unknown", nil)
	w = httptest.NewRecorder()
	rtr.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Errorf("router miss: got %d, want 404 body=%s", w.Code, w.Body.String())
	}

	// /status with the wrong method (POST against a GET-only route) 404
	req = httptest.NewRequest("POST", "/functions/v1/proj_p1/http/status", nil)
	w = httptest.NewRecorder()
	rtr.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Errorf("router method mismatch: got %d, want 404", w.Code)
	}
}
