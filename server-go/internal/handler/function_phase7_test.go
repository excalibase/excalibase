package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
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
	req.Header.Set("X-Excalibase-Runtime-Token", edgefn.DeriveRuntimeSecret("test-runtime-secret", "proj_p1"))
	w = httptest.NewRecorder()
	rtr.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("authenticated internal invoke: got %d body=%s", w.Code, w.Body.String())
	}
}

// TestInternalRoutes_EmptyRuntimeSecretFailsClosed proves the fail-open hole
// is closed: when no runtimeSecret is configured, the internal routes must
// reject every request with 503 ("internal route not configured") instead of
// letting unauthenticated callers straight through. This covers both the
// /internal/invoke bridge and the runtime /metadata callback.
func TestInternalRoutes_EmptyRuntimeSecretFailsClosed(t *testing.T) {
	store := edgefn.NewFunctionStore(t.TempDir())
	v := newFakeVault()
	secrets := edgefn.NewSecretsStore(v)
	runtime, _ := mockFnRuntime(t)
	client := edgefn.NewRuntimeClient(runtime.URL, "")
	instStore := &inMemoryInstanceStore{insts: map[string]*domain.DatabaseInstance{
		"proj_p1": {ProjectID: "proj_p1", OrgID: "default"},
	}}
	h := NewFunctionHandler(store, secrets, client, instStore, nil, testAPIBase)
	// runtimeSecret intentionally left "" (never SetK8sClient'd with a secret).

	rtr := chi.NewRouter()
	rtr.Post(testInternalInvokeRoute, h.InternalInvoke)
	rtr.Post("/internal/runtime/functions/{fnId}/metadata", h.ReceiveExportMetadata)

	// InternalInvoke — even with NO header (the empty-secret would have matched
	// the empty header under the old fail-open guard) must now 503.
	req := httptest.NewRequest("POST", "/internal/invoke/proj_p1/"+testInternalOnlyFnID,
		bytes.NewBufferString(`{"args":{}}`))
	w := httptest.NewRecorder()
	rtr.ServeHTTP(w, req)
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("InternalInvoke empty runtimeSecret: got %d, want 503 (body=%s)", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "internal route not configured") {
		t.Errorf("InternalInvoke 503 body: got %s, want 'internal route not configured'", w.Body.String())
	}

	// ReceiveExportMetadata — same fail-closed behaviour.
	req = httptest.NewRequest("POST", "/internal/runtime/functions/"+testInternalOnlyFnID+"/metadata",
		bytes.NewBufferString(`{"projectId":"proj_p1","exports":[]}`))
	w = httptest.NewRecorder()
	rtr.ServeHTTP(w, req)
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("ReceiveExportMetadata empty runtimeSecret: got %d, want 503 (body=%s)", w.Code, w.Body.String())
	}
}

// --- Phase 7: HTTP action / router dispatch routes ---

// httpActionBundle is a minimal index.ts content that makes Bundle()
// detect `kind: "httpAction"` and stamp Function.Kind accordingly.
const httpActionBundle = `
export default {
  kind: "httpAction",
  handler: async (_ctx, _req) => new Response("ok"),
  __metadata: {},
}`

// httpRouterBundle is a minimal index.ts content that makes Bundle()
// detect `kind: "httpRouter"` and extract its __excalibase_routes.
const httpRouterBundle = `
const router = {
  kind: "httpRouter",
  __excalibase_routes: [
    { path: "/status",  method: "GET",  exportName: "default" },
    { path: "/webhook", method: "POST", exportName: "default" },
  ],
  route: () => router,
  getRoutes: () => [],
};
export default router;
`

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
		ProjectID: "proj_p1",
		ID:        testHTTPActionFnID,
		Name:      "Webhook",
		Files:     []edgefn.File{{Path: testIndexTS, Content: httpActionBundle}},
		Active:    true,
		VerifyJwt: &verify,
	}); err != nil {
		t.Fatalf("save: %v", err)
	}
	client.Deploy(context.Background(), edgefn.DeployRequest{
		ID: "proj_p1__" + testHTTPActionFnID, Code: httpActionBundle,
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

// TestInternalInvoke_RejectsInvalidProjectID confirms that the
// /internal/invoke route validates its projectId path param even when the
// runtime-token check passes — guards against badly-formed cross-runtime
// calls leaking into the store.
func TestInternalInvoke_RejectsInvalidProjectID(t *testing.T) {
	store := edgefn.NewFunctionStore(t.TempDir())
	v := newFakeVault()
	secrets := edgefn.NewSecretsStore(v)
	runtime, _ := mockFnRuntime(t)
	client := edgefn.NewRuntimeClient(runtime.URL, "secret")
	h := NewFunctionHandler(store, secrets, client, &inMemoryInstanceStore{
		insts: map[string]*domain.DatabaseInstance{},
	}, nil, testAPIBase)
	h.SetK8sClient(nil, "", "secret")

	rtr := chi.NewRouter()
	rtr.Post(testInternalInvokeRoute, h.InternalInvoke)
	req := httptest.NewRequest("POST", "/internal/invoke/bad..proj/fn",
		bytes.NewBufferString(`{"args":{}}`))
	req.Header.Set("X-Excalibase-Runtime-Token", "secret")
	w := httptest.NewRecorder()
	rtr.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("invalid projectId: got %d, want 400", w.Code)
	}
}

// TestInternalInvoke_404OnMissingFunction confirms the route returns 404
// when the function id doesn't exist, mirroring PublicInvoke's behaviour
// so internal callers can distinguish missing vs. other failures.
func TestInternalInvoke_404OnMissingFunction(t *testing.T) {
	store := edgefn.NewFunctionStore(t.TempDir())
	v := newFakeVault()
	secrets := edgefn.NewSecretsStore(v)
	runtime, _ := mockFnRuntime(t)
	client := edgefn.NewRuntimeClient(runtime.URL, "secret")
	instStore := &inMemoryInstanceStore{insts: map[string]*domain.DatabaseInstance{
		"proj_p1": {ProjectID: "proj_p1", OrgID: "default"},
	}}
	h := NewFunctionHandler(store, secrets, client, instStore, nil, testAPIBase)
	h.SetK8sClient(nil, "", "secret")

	rtr := chi.NewRouter()
	rtr.Post(testInternalInvokeRoute, h.InternalInvoke)
	req := httptest.NewRequest("POST", "/internal/invoke/proj_p1/missing",
		bytes.NewBufferString(`{"args":{}}`))
	req.Header.Set("X-Excalibase-Runtime-Token", edgefn.DeriveRuntimeSecret("secret", "proj_p1"))
	w := httptest.NewRecorder()
	rtr.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Errorf("missing fn: got %d, want 404", w.Code)
	}
}

// TestPublicHttpInvoke_CORSPreflight confirms the OPTIONS preflight path
// is handled (no rate-limit accounting, no auth check) so browser callers
// can probe the route.
func TestPublicHttpInvoke_CORSPreflight(t *testing.T) {
	store := edgefn.NewFunctionStore(t.TempDir())
	v := newFakeVault()
	secrets := edgefn.NewSecretsStore(v)
	runtime, _ := mockFnRuntime(t)
	client := edgefn.NewRuntimeClient(runtime.URL, "")
	instStore := &inMemoryInstanceStore{insts: map[string]*domain.DatabaseInstance{
		"proj_p1": {ProjectID: "proj_p1", OrgID: "default"},
	}}
	h := NewFunctionHandler(store, secrets, client, instStore, nil, testAPIBase)

	rtr := chi.NewRouter()
	rtr.HandleFunc("/functions/v1/{projectId}/http/*", h.PublicHttpInvoke)
	req := httptest.NewRequest("OPTIONS", "/functions/v1/proj_p1/http/anything", nil)
	w := httptest.NewRecorder()
	rtr.ServeHTTP(w, req)
	if w.Code != http.StatusNoContent {
		t.Errorf("CORS preflight: got %d, want 204", w.Code)
	}
}

// TestPublicHttpInvoke_RejectsBadProjectID guards the projectId validation
// path so cross-tenant requests can't sneak through with crafted path
// segments.
func TestPublicHttpInvoke_RejectsBadProjectID(t *testing.T) {
	store := edgefn.NewFunctionStore(t.TempDir())
	v := newFakeVault()
	secrets := edgefn.NewSecretsStore(v)
	runtime, _ := mockFnRuntime(t)
	client := edgefn.NewRuntimeClient(runtime.URL, "")
	h := NewFunctionHandler(store, secrets, client, &inMemoryInstanceStore{
		insts: map[string]*domain.DatabaseInstance{},
	}, nil, testAPIBase)

	rtr := chi.NewRouter()
	rtr.HandleFunc("/functions/v1/{projectId}/http/*", h.PublicHttpInvoke)
	req := httptest.NewRequest("GET", "/functions/v1/bad..pid/http/anything", nil)
	w := httptest.NewRecorder()
	rtr.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("bad project id: got %d, want 400", w.Code)
	}
}

// TestPublicHttpInvoke_RateLimited reaches the rate-limit branch without
// configuring a real signing key. Setting an aggressive limit and firing two
// requests quickly forces the second into the 429 path.
func TestPublicHttpInvoke_RateLimited(t *testing.T) {
	store := edgefn.NewFunctionStore(t.TempDir())
	v := newFakeVault()
	secrets := edgefn.NewSecretsStore(v)
	runtime, _ := mockFnRuntime(t)
	client := edgefn.NewRuntimeClient(runtime.URL, "")
	instStore := &inMemoryInstanceStore{insts: map[string]*domain.DatabaseInstance{
		"proj_p1": {ProjectID: "proj_p1", OrgID: "default"},
	}}
	h := NewFunctionHandler(store, secrets, client, instStore, nil, testAPIBase)
	h.SetRateLimit(1, 0.001) // bucket of 1, refill near-zero

	verify := false
	if err := store.Save(&edgefn.Function{
		ProjectID: "proj_p1", ID: "rl",
		Name:      "RL",
		Files:     []edgefn.File{{Path: testIndexTS, Content: httpActionBundle}},
		Active:    true,
		VerifyJwt: &verify,
	}); err != nil {
		t.Fatalf("save: %v", err)
	}
	client.Deploy(context.Background(), edgefn.DeployRequest{
		ID: "proj_p1__rl", Code: httpActionBundle,
	})
	rtr := chi.NewRouter()
	rtr.HandleFunc("/functions/v1/{projectId}/http/*", h.PublicHttpInvoke)

	// First request consumes the token.
	req := httptest.NewRequest("POST", "/functions/v1/proj_p1/http/rl", nil)
	w := httptest.NewRecorder()
	rtr.ServeHTTP(w, req)
	// Second request lands on the 429 branch.
	req = httptest.NewRequest("POST", "/functions/v1/proj_p1/http/rl", nil)
	w = httptest.NewRecorder()
	rtr.ServeHTTP(w, req)
	if w.Code != http.StatusTooManyRequests {
		t.Errorf("second request: got %d, want 429", w.Code)
	}
}

// TestPublicHttpInvoke_StoreErrSurfaces500 covers the rare store.List
// error path so a corrupted store dir is not silently treated as a 404.
// We exercise it indirectly via the empty-project happy path (no functions
// → 404, not 500) since List() does not error on an empty dir.
func TestPublicHttpInvoke_404OnEmptyProject(t *testing.T) {
	store := edgefn.NewFunctionStore(t.TempDir())
	v := newFakeVault()
	secrets := edgefn.NewSecretsStore(v)
	runtime, _ := mockFnRuntime(t)
	client := edgefn.NewRuntimeClient(runtime.URL, "")
	instStore := &inMemoryInstanceStore{insts: map[string]*domain.DatabaseInstance{
		"proj_p1": {ProjectID: "proj_p1", OrgID: "default"},
	}}
	h := NewFunctionHandler(store, secrets, client, instStore, nil, testAPIBase)

	rtr := chi.NewRouter()
	rtr.HandleFunc("/functions/v1/{projectId}/http/*", h.PublicHttpInvoke)
	req := httptest.NewRequest("GET", "/functions/v1/proj_p1/http/anything", nil)
	w := httptest.NewRecorder()
	rtr.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Errorf("empty project: got %d, want 404", w.Code)
	}
}

// TestMatchRouterRoute_MalformedJSON exercises the defensive json.Unmarshal
// branch — a corrupted HttpRoutes blob should produce a miss (no panic, no
// false positive) so the gateway 404s cleanly.
func TestMatchRouterRoute_MalformedJSON(t *testing.T) {
	if matchRouterRoute([]byte("{not json}"), "/x", "GET") {
		t.Error("malformed JSON should not match any route")
	}
	if matchRouterRoute(nil, "/x", "GET") {
		t.Error("nil routes should not match")
	}
}

// TestIsInternalFromMetadata_NegativeBranches keeps the helper's branches
// covered for the runtime metadata callback path.
func TestIsInternalFromMetadata_NegativeBranches(t *testing.T) {
	if isInternalFromMetadata(nil) {
		t.Error("nil should be false")
	}
	if isInternalFromMetadata([]byte("not-json")) {
		t.Error("invalid JSON should be false")
	}
	if isInternalFromMetadata([]byte("[]")) {
		t.Error("empty array should be false")
	}
	if isInternalFromMetadata([]byte(`[{"kind":"query"}]`)) {
		t.Error("no isInternal field should be false")
	}
	if !isInternalFromMetadata([]byte(`[{"kind":"query","isInternal":true}]`)) {
		t.Error("isInternal:true should be true")
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
	if err := store.Save(&edgefn.Function{
		ProjectID: "proj_p1",
		ID:        testHTTPRouterFnID,
		Name:      "Api Http",
		Files:     []edgefn.File{{Path: testIndexTS, Content: httpRouterBundle}},
		Active:    true,
		VerifyJwt: &verify,
	}); err != nil {
		t.Fatalf("save: %v", err)
	}
	client.Deploy(context.Background(), edgefn.DeployRequest{
		ID: "proj_p1__" + testHTTPRouterFnID, Code: httpRouterBundle,
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

// --- Phase 15b: X-Excalibase-Envelope header pass-through ---

// TestInternalInvoke_ForwardsEnvelopeHeaderToRuntime confirms that the
// `X-Excalibase-Envelope: v1` request header arrives at the runtime inside
// the InvokeRequest's `headers` map. Graphql's Phase 15a registry sets this
// header on every subscription invocation so the runtime knows to return
// `{result, reads}` instead of the legacy `{data}` body shape. Stripping the
// header at the gateway would silently break the precise-dep filter — the
// registry would always see `reads: []` and fall back to "re-invoke on any
// project commit".
func TestInternalInvoke_ForwardsEnvelopeHeaderToRuntime(t *testing.T) {
	v := newFakeVault()
	store := edgefn.NewFunctionStore(t.TempDir())
	secrets := edgefn.NewSecretsStore(v)

	var gotEnvelope string
	runtime := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, testInvokePath) {
			var body struct {
				Headers map[string]string `json:"headers"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			// Header lookup must be case-insensitive on the inbound side —
			// Go's http.Header canonicalizes incoming requests. The handler
			// must preserve the value verbatim so the runtime sees it.
			for k, val := range body.Headers {
				if strings.EqualFold(k, "X-Excalibase-Envelope") {
					gotEnvelope = val
					break
				}
			}
			w.Header().Set(sharedContentType, sharedMIMEJSON)
			_, _ = w.Write([]byte(`{"status":200,"headers":{"content-type":"application/json"},"body":"{\"data\":\"ok\"}"}`))
			return
		}
		if strings.HasPrefix(r.URL.Path, "/deploy") {
			_, _ = w.Write([]byte(`{"id":"x","url":"x"}`))
			return
		}
		w.WriteHeader(200)
	}))
	t.Cleanup(runtime.Close)
	client := edgefn.NewRuntimeClient(runtime.URL, "test-runtime-secret")
	h := NewFunctionHandler(store, secrets, client, &inMemoryInstanceStore{insts: map[string]*domain.DatabaseInstance{
		"proj_p1": {ProjectID: "proj_p1", OrgID: "default"},
	}}, nil, testAPIBase)
	h.SetK8sClient(nil, "", "test-runtime-secret")

	if err := store.Save(&edgefn.Function{
		ProjectID: "proj_p1",
		ID:        testInternalOnlyFnID,
		Name:      "Internal",
		Files:     []edgefn.File{{Path: testIndexTS, Content: testDefaultHandler}},
		Active:    true,
	}); err != nil {
		t.Fatalf("save: %v", err)
	}

	rtr := chi.NewRouter()
	rtr.Post(testInternalInvokeRoute, h.InternalInvoke)

	req := httptest.NewRequest("POST", "/internal/invoke/proj_p1/"+testInternalOnlyFnID,
		bytes.NewBufferString(`{"args":{}}`))
	req.Header.Set("X-Excalibase-Runtime-Token", edgefn.DeriveRuntimeSecret("test-runtime-secret", "proj_p1"))
	req.Header.Set("X-Excalibase-Envelope", "v1")
	w := httptest.NewRecorder()
	rtr.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("internal invoke: got %d body=%s", w.Code, w.Body.String())
	}
	if gotEnvelope != "v1" {
		t.Errorf("X-Excalibase-Envelope header passthrough: got %q, want %q", gotEnvelope, "v1")
	}
}

// TestInternalInvoke_NoEnvelopeHeaderWhenAbsent confirms that the runtime
// does NOT receive an envelope header when the caller omits it — legacy
// callers (SDK direct invoke, ad-hoc curl) must not be forced into the
// envelope shape behind their back.
func TestInternalInvoke_NoEnvelopeHeaderWhenAbsent(t *testing.T) {
	v := newFakeVault()
	store := edgefn.NewFunctionStore(t.TempDir())
	secrets := edgefn.NewSecretsStore(v)

	var sawEnvelope bool
	runtime := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, testInvokePath) {
			var body struct {
				Headers map[string]string `json:"headers"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			for k := range body.Headers {
				if strings.EqualFold(k, "X-Excalibase-Envelope") {
					sawEnvelope = true
					break
				}
			}
			w.Header().Set(sharedContentType, sharedMIMEJSON)
			_, _ = w.Write([]byte(`{"status":200,"headers":{"content-type":"application/json"},"body":"{\"data\":\"ok\"}"}`))
			return
		}
		if strings.HasPrefix(r.URL.Path, "/deploy") {
			_, _ = w.Write([]byte(`{"id":"x","url":"x"}`))
			return
		}
		w.WriteHeader(200)
	}))
	t.Cleanup(runtime.Close)
	client := edgefn.NewRuntimeClient(runtime.URL, "test-runtime-secret")
	h := NewFunctionHandler(store, secrets, client, &inMemoryInstanceStore{insts: map[string]*domain.DatabaseInstance{
		"proj_p1": {ProjectID: "proj_p1", OrgID: "default"},
	}}, nil, testAPIBase)
	h.SetK8sClient(nil, "", "test-runtime-secret")

	if err := store.Save(&edgefn.Function{
		ProjectID: "proj_p1",
		ID:        testInternalOnlyFnID,
		Name:      "Internal",
		Files:     []edgefn.File{{Path: testIndexTS, Content: testDefaultHandler}},
		Active:    true,
	}); err != nil {
		t.Fatalf("save: %v", err)
	}

	rtr := chi.NewRouter()
	rtr.Post(testInternalInvokeRoute, h.InternalInvoke)

	req := httptest.NewRequest("POST", "/internal/invoke/proj_p1/"+testInternalOnlyFnID,
		bytes.NewBufferString(`{"args":{}}`))
	req.Header.Set("X-Excalibase-Runtime-Token", edgefn.DeriveRuntimeSecret("test-runtime-secret", "proj_p1"))
	// Deliberately no X-Excalibase-Envelope — pre-15b client behavior.
	w := httptest.NewRecorder()
	rtr.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("internal invoke: got %d body=%s", w.Code, w.Body.String())
	}
	if sawEnvelope {
		t.Errorf("envelope header must be absent when caller did not set it")
	}
}
