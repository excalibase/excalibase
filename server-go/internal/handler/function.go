package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/excalibase/provisioning-poc/internal/edgefn"
	"github.com/excalibase/provisioning-poc/internal/k8s"
	"github.com/excalibase/provisioning-poc/internal/storage"
	"github.com/go-chi/chi/v5"
)

// FunctionHandler exposes per-project edge function CRUD + invoke + secrets.
// Routes are all scoped under /api/projects/{projectId}/functions and use the
// new multi-file Function model.
//
// In per-project deployment mode, each project gets its own deno-runtime pod
// in its own namespace. The handler:
//   - lazy-creates the pod on first function deploy via k8s.EnsureDenoRuntime
//   - builds a per-project RuntimeClient pointing at the in-namespace service
//   - caches clients per projectId
type FunctionHandler struct {
	store         *edgefn.FunctionStore
	secrets       *edgefn.SecretsStore
	instanceStore storage.InstanceStore
	orgStore      storage.OrgStore
	publicBaseURL string

	// Per-project runtime fan-out
	k8sClient     k8s.KubeClient
	runtimeImage  string
	runtimeSecret string
	runtimeURLFn  func(namespace string) string // tests override; nil → cluster DNS
	clientMu      sync.Mutex
	clients       map[string]*edgefn.RuntimeClient // keyed by projectId

	// Rate limiting for the public invoke route. Token bucket per project.
	limiterMu     sync.Mutex
	limiters      map[string]*tokenBucket
	rateBurst     int
	ratePerSecond float64
}

// tokenBucket is a minimal in-memory leaky-bucket limiter. One per project.
// Not exported — callers configure via SetRateLimit on the handler.
type tokenBucket struct {
	mu          sync.Mutex
	capacity    float64
	tokens      float64
	refillRate  float64
	lastRefill  time.Time
}

func newTokenBucket(capacity int, refillPerSecond float64) *tokenBucket {
	return &tokenBucket{
		capacity:   float64(capacity),
		tokens:     float64(capacity),
		refillRate: refillPerSecond,
		lastRefill: time.Now(),
	}
}

// take consumes 1 token. Returns true if granted, false if exhausted.
func (b *tokenBucket) take() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	now := time.Now()
	elapsed := now.Sub(b.lastRefill).Seconds()
	b.tokens += elapsed * b.refillRate
	if b.tokens > b.capacity {
		b.tokens = b.capacity
	}
	b.lastRefill = now
	if b.tokens >= 1 {
		b.tokens--
		return true
	}
	return false
}

// NewFunctionHandler constructs a per-project handler. Pass nil for k8sClient
// in unit tests; in that case the legacy shared-client path is used (set via
// SetSharedClient). For production, call SetK8sClient with a real client.
func NewFunctionHandler(
	store *edgefn.FunctionStore,
	secrets *edgefn.SecretsStore,
	client *edgefn.RuntimeClient,
	instanceStore storage.InstanceStore,
	orgStore storage.OrgStore,
	publicBaseURL string,
) *FunctionHandler {
	h := &FunctionHandler{
		store:         store,
		secrets:       secrets,
		instanceStore: instanceStore,
		orgStore:      orgStore,
		publicBaseURL: publicBaseURL,
		clients:       make(map[string]*edgefn.RuntimeClient),
		limiters:      make(map[string]*tokenBucket),
		rateBurst:     100,
		ratePerSecond: 100,
	}
	// Backwards-compat: if a single shared client is supplied, use it for all
	// projects until k8sClient is set.
	if client != nil {
		h.clients["__shared__"] = client
	}
	return h
}

// SetK8sClient enables per-project Deno runtime provisioning. When set, the
// handler will lazy-create a deno-runtime pod in each project's namespace on
// first function deploy and build a project-scoped RuntimeClient.
func (h *FunctionHandler) SetK8sClient(c k8s.KubeClient, image, runtimeSecret string) {
	h.k8sClient = c
	h.runtimeImage = image
	h.runtimeSecret = runtimeSecret
}

// SetRuntimeURLFn overrides cluster DNS URL resolution. Used by integration
// tests to map namespaces to localhost subprocesses. Pass a function that
// takes a namespace and returns the runtime base URL.
func (h *FunctionHandler) SetRuntimeURLFn(fn func(namespace string) string) {
	h.runtimeURLFn = fn
}

// runtimeClientFor returns (creating if needed) a RuntimeClient for the given
// project. In per-project mode it points at the in-namespace deno-runtime
// service; in shared mode it returns the single client supplied at construction.
func (h *FunctionHandler) runtimeClientFor(ctx context.Context, projectID string) (*edgefn.RuntimeClient, error) {
	h.clientMu.Lock()
	if c, ok := h.clients[projectID]; ok {
		h.clientMu.Unlock()
		return c, nil
	}
	if shared, ok := h.clients["__shared__"]; ok && h.k8sClient == nil {
		h.clientMu.Unlock()
		return shared, nil
	}
	h.clientMu.Unlock()

	// Per-project path — need namespace and to ensure the runtime exists.
	if h.k8sClient == nil {
		return nil, fmt.Errorf("no runtime client available")
	}
	namespace := h.namespaceFor(projectID)
	if namespace == "" {
		return nil, fmt.Errorf("no namespace for project %s", projectID)
	}
	if err := h.k8sClient.EnsureDenoRuntime(ctx, namespace, h.runtimeImage, h.runtimeSecret); err != nil {
		return nil, fmt.Errorf("ensure deno runtime: %w", err)
	}

	var url string
	if h.runtimeURLFn != nil {
		url = h.runtimeURLFn(namespace)
	} else {
		url = fmt.Sprintf("http://deno-runtime.%s.svc.cluster.local:8000", namespace)
	}
	client := edgefn.NewRuntimeClient(url, h.runtimeSecret)

	h.clientMu.Lock()
	h.clients[projectID] = client
	h.clientMu.Unlock()
	return client, nil
}

// namespaceFor resolves the K8s namespace for a project from the instance store.
// Falls back to {orgId}-{projectId} if the instance has no explicit namespace.
func (h *FunctionHandler) namespaceFor(projectID string) string {
	if h.instanceStore == nil {
		return ""
	}
	inst, err := h.instanceStore.FindByProjectID(projectID)
	if err != nil || inst == nil {
		return ""
	}
	if inst.Namespace != "" {
		return inst.Namespace
	}
	if inst.OrgID != "" {
		return inst.OrgID + "-" + projectID
	}
	return ""
}

// SetRateLimit configures the public invoke rate limit (per project).
//   burst:           initial bucket size — how many requests can be made instantly
//   refillPerSecond: refill rate (≈ steady-state requests per second)
// Default is 100/100 — generous for normal use, blocks runaway loops.
func (h *FunctionHandler) SetRateLimit(burst int, refillPerSecond float64) {
	h.limiterMu.Lock()
	defer h.limiterMu.Unlock()
	h.rateBurst = burst
	h.ratePerSecond = refillPerSecond
	// Reset existing buckets so the new limit applies immediately.
	h.limiters = make(map[string]*tokenBucket)
}

// allowProject checks the rate limit for a given project. Lazily creates the
// bucket on first request.
func (h *FunctionHandler) allowProject(projectID string) bool {
	h.limiterMu.Lock()
	bucket, ok := h.limiters[projectID]
	if !ok {
		bucket = newTokenBucket(h.rateBurst, h.ratePerSecond)
		h.limiters[projectID] = bucket
	}
	h.limiterMu.Unlock()
	return bucket.take()
}

// orgSlugFor resolves the org slug for a given project id so secrets and vault
// paths stay consistent with the rest of the platform.
func (h *FunctionHandler) orgSlugFor(ctx context.Context, projectID string) string {
	if h.instanceStore == nil {
		return "default"
	}
	inst, err := h.instanceStore.FindByProjectID(projectID)
	if err != nil || inst == nil {
		return "default"
	}
	if h.orgStore == nil || inst.OrgID == "" {
		return "default"
	}
	org, err := h.orgStore.FindOrgByID(ctx, inst.OrgID)
	if err != nil || org == nil {
		return "default"
	}
	return org.Slug
}

// builtinEnv returns platform-injected env vars for a function deploy.
// These override any user secret with the same key.
func (h *FunctionHandler) builtinEnv(orgSlug, projectID string) map[string]string {
	base := h.publicBaseURL
	if base == "" {
		base = "https://api.excalibase.io"
	}
	return map[string]string{
		"EXCALIBASE_URL":        fmt.Sprintf("%s/%s/%s", base, orgSlug, projectID),
		"EXCALIBASE_PROJECT_ID": projectID,
		"EXCALIBASE_ORG_SLUG":   orgSlug,
	}
}

// List returns all functions belonging to the project.
func (h *FunctionHandler) List(w http.ResponseWriter, r *http.Request) {
	projectID := chi.URLParam(r, "projectId")
	if err := edgefn.ValidateProjectID(projectID); err != nil {
		httpError(w, safeError(err), http.StatusBadRequest)
		return
	}
	list, err := h.store.List(projectID)
	if err != nil {
		httpError(w, safeError(err), http.StatusInternalServerError)
		return
	}
	writeJSON(w, list)
}

// Create (or update) a function by uploading its files.
// Request body: Function struct — id, name, description, files[]
func (h *FunctionHandler) Create(w http.ResponseWriter, r *http.Request) {
	projectID := chi.URLParam(r, "projectId")
	if err := edgefn.ValidateProjectID(projectID); err != nil {
		httpError(w, safeError(err), http.StatusBadRequest)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, int64(edgefn.MaxCodeSize+16*1024))
	var fn edgefn.Function
	if err := json.NewDecoder(r.Body).Decode(&fn); err != nil {
		httpError(w, "invalid request body", http.StatusBadRequest)
		return
	}
	fn.ProjectID = projectID
	fn.Active = true

	if err := h.store.Save(&fn); err != nil {
		httpError(w, safeError(err), http.StatusBadRequest)
		return
	}

	// Bundle + deploy to runtime with merged secrets.
	code, err := fn.Bundle()
	if err != nil {
		httpError(w, safeError(err), http.StatusInternalServerError)
		return
	}
	orgSlug := h.orgSlugFor(r.Context(), projectID)
	env, err := h.secrets.BuildEnvForDeploy(orgSlug, projectID, h.builtinEnv(orgSlug, projectID))
	if err != nil {
		log.Printf("WARN: build env for %s/%s: %v", projectID, fn.ID, err)
		env = h.builtinEnv(orgSlug, projectID)
	}

	client, err := h.runtimeClientFor(r.Context(), projectID)
	if err != nil {
		_ = h.store.Delete(projectID, fn.ID)
		httpError(w, "runtime unavailable: "+safeError(err), http.StatusServiceUnavailable)
		return
	}
	deployErr := client.Deploy(r.Context(), edgefn.DeployRequest{
		ID:      fn.RuntimeID(),
		Code:    code,
		Secrets: env,
	})
	if deployErr != nil {
		// Rollback the store record — the deploy didn't land, so we shouldn't
		// keep a stale function around.
		if delErr := h.store.Delete(projectID, fn.ID); delErr != nil {
			log.Printf("WARN: rollback function store: %v", delErr)
		}
		httpError(w, "failed to deploy: "+safeError(deployErr), http.StatusBadGateway)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(fn)
}

// Get returns a single function (with all its files).
func (h *FunctionHandler) Get(w http.ResponseWriter, r *http.Request) {
	projectID := chi.URLParam(r, "projectId")
	fnID := chi.URLParam(r, "fnId")
	fn, err := h.store.Get(projectID, fnID)
	if err != nil {
		httpError(w, safeError(err), http.StatusBadRequest)
		return
	}
	if fn == nil {
		httpError(w, "function not found", http.StatusNotFound)
		return
	}
	writeJSON(w, fn)
}

// Delete removes the function from both store and runtime.
func (h *FunctionHandler) Delete(w http.ResponseWriter, r *http.Request) {
	projectID := chi.URLParam(r, "projectId")
	fnID := chi.URLParam(r, "fnId")
	fn, err := h.store.Get(projectID, fnID)
	if err != nil {
		httpError(w, safeError(err), http.StatusBadRequest)
		return
	}
	if fn == nil {
		httpError(w, "function not found", http.StatusNotFound)
		return
	}
	if client, cerr := h.runtimeClientFor(r.Context(), projectID); cerr == nil {
		if err := client.Delete(r.Context(), fn.RuntimeID()); err != nil {
			log.Printf("WARN: runtime delete %s: %v", fn.RuntimeID(), err)
		}
	}
	if err := h.store.Delete(projectID, fnID); err != nil {
		httpError(w, safeError(err), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]string{"status": "deleted", "id": fnID})
}

// Invoke is the admin test path — uses the same forwarding logic as PublicInvoke
// but strips Authorization/Cookie headers (admin testing should not leak the
// admin's session token to user code).
func (h *FunctionHandler) Invoke(w http.ResponseWriter, r *http.Request) {
	projectID := chi.URLParam(r, "projectId")
	fnID := chi.URLParam(r, "fnId")
	fn, err := h.store.Get(projectID, fnID)
	if err != nil {
		httpError(w, safeError(err), http.StatusBadRequest)
		return
	}
	if fn == nil {
		httpError(w, "function not found", http.StatusNotFound)
		return
	}
	h.forwardToRuntime(w, r, fn, true /* stripAuth */)
}

// PublicInvoke is the Supabase-style public route /functions/v1/{projectId}/{fnId}.
// Enforces:
//   1. Per-project rate limit (returns 429 when exceeded)
//   2. JWT presence check if the function has VerifyJwt enabled (default true)
//   3. Forwards Authorization header to the function so user code can inspect it
func (h *FunctionHandler) PublicInvoke(w http.ResponseWriter, r *http.Request) {
	projectID := chi.URLParam(r, "projectId")
	fnID := chi.URLParam(r, "fnId")

	if !h.allowProject(projectID) {
		w.Header().Set("Retry-After", "1")
		httpError(w, "rate limit exceeded", http.StatusTooManyRequests)
		return
	}

	fn, err := h.store.Get(projectID, fnID)
	if err != nil || fn == nil {
		// Don't distinguish project-missing from function-missing — both 404 to
		// avoid leaking which projects exist.
		httpError(w, "function not found", http.StatusNotFound)
		return
	}

	if fn.JwtVerificationRequired() {
		authHeader := r.Header.Get("Authorization")
		if authHeader == "" || !strings.HasPrefix(authHeader, "Bearer ") {
			httpError(w, "missing or invalid Authorization header", http.StatusUnauthorized)
			return
		}
		// NOTE: signature validation is the responsibility of the user function
		// or a future JWKS-aware middleware. We only enforce header presence.
		// This matches Supabase's `verify_jwt: false` opt-out shape but with
		// an intentionally simpler check. Document loudly in the studio UI.
	}

	h.forwardToRuntime(w, r, fn, false /* keep Authorization */)
}

// forwardToRuntime serializes the incoming HTTP request and ships it to the
// Deno runtime via the RuntimeClient. The runtime's response is written back
// to w with status, headers, and body intact.
func (h *FunctionHandler) forwardToRuntime(w http.ResponseWriter, r *http.Request, fn *edgefn.Function, stripAuth bool) {
	r.Body = http.MaxBytesReader(w, r.Body, 1024*1024)
	bodyBytes, err := io.ReadAll(r.Body)
	if err != nil {
		httpError(w, "failed to read body", http.StatusBadRequest)
		return
	}

	headers := make(map[string]string, len(r.Header))
	for k, v := range r.Header {
		if len(v) > 0 {
			headers[k] = v[0]
		}
	}
	if stripAuth {
		delete(headers, "Authorization")
		delete(headers, "Cookie")
	}

	invokeReq := edgefn.InvokeRequest{
		Method:  r.Method,
		URL:     r.URL.String(),
		Headers: headers,
		Body:    string(bodyBytes),
	}

	client, err := h.runtimeClientFor(r.Context(), fn.ProjectID)
	if err != nil {
		httpError(w, "runtime unavailable: "+safeError(err), http.StatusServiceUnavailable)
		return
	}
	resp, err := client.Invoke(r.Context(), fn.RuntimeID(), invokeReq)
	if err != nil {
		httpError(w, safeError(err), http.StatusInternalServerError)
		return
	}
	writeRuntimeResponse(w, resp)
}

// writeRuntimeResponse forwards a runtime InvokeResponse back to the caller
// with the handler's original status, headers, and body.
func writeRuntimeResponse(w http.ResponseWriter, resp *edgefn.InvokeResponse) {
	for k, v := range resp.Headers {
		// Drop hop-by-hop headers.
		lk := strings.ToLower(k)
		if lk == "transfer-encoding" || lk == "connection" || lk == "content-length" {
			continue
		}
		w.Header().Set(k, v)
	}
	status := resp.Status
	if status == 0 {
		status = 200
	}
	w.WriteHeader(status)
	_, _ = io.WriteString(w, resp.Body)
}

// --- Secrets ---

// ListSecrets returns secret keys only (never values). Values are write-only
// from the admin surface — matches Supabase behavior.
func (h *FunctionHandler) ListSecrets(w http.ResponseWriter, r *http.Request) {
	projectID := chi.URLParam(r, "projectId")
	if err := edgefn.ValidateProjectID(projectID); err != nil {
		httpError(w, safeError(err), http.StatusBadRequest)
		return
	}
	orgSlug := h.orgSlugFor(r.Context(), projectID)
	keys, err := h.secrets.ListKeys(orgSlug, projectID)
	if err != nil {
		httpError(w, safeError(err), http.StatusInternalServerError)
		return
	}
	out := make([]map[string]string, 0, len(keys))
	for _, k := range keys {
		out = append(out, map[string]string{"key": k})
	}
	writeJSON(w, out)
}

// SetSecret stores a single key/value. Redeploys all functions of the project
// so the new value takes effect immediately.
func (h *FunctionHandler) SetSecret(w http.ResponseWriter, r *http.Request) {
	projectID := chi.URLParam(r, "projectId")
	if err := edgefn.ValidateProjectID(projectID); err != nil {
		httpError(w, safeError(err), http.StatusBadRequest)
		return
	}
	var body struct {
		Key   string `json:"key"`
		Value string `json:"value"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httpError(w, "invalid request body", http.StatusBadRequest)
		return
	}
	orgSlug := h.orgSlugFor(r.Context(), projectID)
	if err := h.secrets.Set(orgSlug, projectID, body.Key, body.Value); err != nil {
		httpError(w, safeError(err), http.StatusBadRequest)
		return
	}
	h.redeployAll(r, projectID, orgSlug)
	writeJSON(w, map[string]string{"status": "set", "key": body.Key})
}

// DeleteSecret removes a secret and redeploys the project's functions.
func (h *FunctionHandler) DeleteSecret(w http.ResponseWriter, r *http.Request) {
	projectID := chi.URLParam(r, "projectId")
	key := chi.URLParam(r, "key")
	orgSlug := h.orgSlugFor(r.Context(), projectID)
	if err := h.secrets.Delete(orgSlug, projectID, key); err != nil {
		httpError(w, safeError(err), http.StatusBadRequest)
		return
	}
	h.redeployAll(r, projectID, orgSlug)
	writeJSON(w, map[string]string{"status": "deleted", "key": key})
}

// redeployAll re-pushes every function of the project to the runtime so the
// latest env (user secrets + builtins) is picked up. Errors are logged and
// not surfaced — secret write succeeded, so the admin UI should still 200.
func (h *FunctionHandler) redeployAll(r *http.Request, projectID, orgSlug string) {
	list, err := h.store.List(projectID)
	if err != nil || len(list) == 0 {
		return
	}
	env, err := h.secrets.BuildEnvForDeploy(orgSlug, projectID, h.builtinEnv(orgSlug, projectID))
	if err != nil {
		log.Printf("WARN: redeploy build env: %v", err)
		return
	}
	client, cerr := h.runtimeClientFor(r.Context(), projectID)
	if cerr != nil {
		log.Printf("WARN: redeploy runtime client: %v", cerr)
		return
	}
	for _, fn := range list {
		code, err := fn.Bundle()
		if err != nil {
			log.Printf("WARN: redeploy bundle %s: %v", fn.ID, err)
			continue
		}
		if err := client.Deploy(r.Context(), edgefn.DeployRequest{
			ID: fn.RuntimeID(), Code: code, Secrets: env,
		}); err != nil {
			log.Printf("WARN: redeploy %s: %v", fn.RuntimeID(), err)
		}
	}
}

// RuntimeStatus reports whether the project's runtime is reachable.
func (h *FunctionHandler) RuntimeStatus(w http.ResponseWriter, r *http.Request) {
	projectID := chi.URLParam(r, "projectId")
	client, err := h.runtimeClientFor(r.Context(), projectID)
	if err != nil {
		writeJSON(w, map[string]interface{}{"status": "unavailable", "healthy": false})
		return
	}
	healthy, err := client.Health(r.Context())
	status := "healthy"
	if err != nil || !healthy {
		status = "unavailable"
	}
	writeJSON(w, map[string]interface{}{"status": status, "healthy": healthy})
}
