package handler

import (
	"context"
	"crypto/ecdsa"
	"crypto/subtle"
	"crypto/x509"
	"database/sql"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"log"
	"maps"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/excalibase/provisioning-poc/internal/edgefn"
	"github.com/excalibase/provisioning-poc/internal/k8s"
	custommw "github.com/excalibase/provisioning-poc/internal/middleware"
	"github.com/excalibase/provisioning-poc/internal/storage"
	"github.com/excalibase/provisioning-poc/internal/vaultclient"
	"github.com/go-chi/chi/v5"
	"github.com/golang-jwt/jwt/v5"
)

const errFunctionNotFound = "function not found"

const errInvalidRequestBody = "invalid request body"

// sharedClientKey is the clients-map slot for the single runtime used when no
// k8s client is configured (self-hosted / docker mode).
const sharedClientKey = "__shared__"

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
	store         edgefn.Store
	secrets       *edgefn.SecretsStore
	vault         vaultclient.VaultClient // optional, for reading DB_URL + JWT tokens
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

	// JWT signing public key cache. Fetched from vault path pki/signing/public
	// on demand and refreshed every ~5 minutes. Protected by jwkMu.
	jwkMu     sync.RWMutex
	jwkKey    *ecdsa.PublicKey
	jwkLoaded time.Time

	// expectedJWTIssuer, if non-empty, must match the JWT's `iss` claim
	// exactly. Empty disables the check (leaves projectId-claim binding as
	// the only tenant gate). Auth service hardcodes "excalibase".
	expectedJWTIssuer string

	// projectDBFn resolves a *sql.DB for the given projectID — used by the
	// schema migrator on deploy. Pluggable so tests can inject a testcontainer
	// DB. nil disables schema migration entirely (deploys still succeed,
	// schema is stored but never applied).
	projectDBFn func(ctx context.Context, projectID string) (*sql.DB, error)

	// autoMigrate gates whether ApplySchema runs at deploy time. Default
	// true; flip to false (EXCALIBASE_AUTO_MIGRATE=false) to defer migration
	// to an explicit POST /api/projects/{projectId}/schema/apply call.
	autoMigrate bool

	// egressStore holds each project's outbound allowlist; egressDefaults is
	// the operator-level list merged into every project (EXC-348). nil store
	// = the allowlist API is unavailable and runtimes get defaults only.
	egressStore    edgefn.EgressStore
	egressDefaults []string

	// requireAud / audPrefix implement the EXC-11 audience binding. Set at
	// construction to fail closed; see SetAudienceRequirement.
	requireAud bool
	audPrefix  string
}

// SetExpectedJWTIssuer configures the iss claim the function handler will
// require on inbound JWTs. Empty (default) disables the check.
func (h *FunctionHandler) SetExpectedJWTIssuer(iss string) {
	h.expectedJWTIssuer = iss
}

// DefaultAudPrefix is what the auth service puts in front of the projectId in
// the aud claim.
const DefaultAudPrefix = "excalibase:"

// SetAudienceRequirement configures the EXC-11 audience binding: every end-user
// token must carry audPrefix+projectId in its aud claim. Handlers require the
// audience from construction, so turning it off is always a deliberate act;
// main.go passes the operator's JWT_REQUIRE_AUD (default true). A blank prefix
// falls back to DefaultAudPrefix so a missing config value can never weaken the
// check to a bare projectId.
func (h *FunctionHandler) SetAudienceRequirement(requireAud bool, audPrefix string) {
	h.requireAud = requireAud
	h.audPrefix = DefaultAudPrefix
	if audPrefix != "" {
		h.audPrefix = audPrefix
	}
}

// tokenBucket is a minimal in-memory leaky-bucket limiter. One per project.
// Not exported — callers configure via SetRateLimit on the handler.
type tokenBucket struct {
	mu         sync.Mutex
	capacity   float64
	tokens     float64
	refillRate float64
	lastRefill time.Time
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
	store edgefn.Store,
	secrets *edgefn.SecretsStore,
	client *edgefn.RuntimeClient,
	instanceStore storage.InstanceStore,
	orgStore storage.OrgStore,
	publicBaseURL string,
) *FunctionHandler {
	h := &FunctionHandler{
		// Fail closed: a handler built without an explicit decision still
		// requires the project audience.
		requireAud:    true,
		audPrefix:     DefaultAudPrefix,
		store:         store,
		secrets:       secrets,
		instanceStore: instanceStore,
		orgStore:      orgStore,
		publicBaseURL: publicBaseURL,
		clients:       make(map[string]*edgefn.RuntimeClient),
		limiters:      make(map[string]*tokenBucket),
		rateBurst:     100,
		ratePerSecond: 100,
		// Auto-migrate defaults to true so deploys are atomic.
		// EXCALIBASE_AUTO_MIGRATE=false defers schema application.
		autoMigrate: !strings.EqualFold(os.Getenv("EXCALIBASE_AUTO_MIGRATE"), "false"),
	}
	// Backwards-compat: if a single shared client is supplied, use it for all
	// projects until k8sClient is set.
	if client != nil {
		h.clients[sharedClientKey] = client
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

// SetVault wires the platform vault client so the handler can read project
// credentials (for EXCALIBASE_DB_URL) and JWT tokens (for ANON/SERVICE keys)
// at function deploy time.
func (h *FunctionHandler) SetVault(v vaultclient.VaultClient) {
	h.vault = v
}

// SetProjectDBFn registers a resolver that returns a *sql.DB for the given
// projectID. The schema migrator uses this to apply user-declared schemas at
// deploy time. nil disables migration entirely.
func (h *FunctionHandler) SetProjectDBFn(fn func(ctx context.Context, projectID string) (*sql.DB, error)) {
	h.projectDBFn = fn
}

// SetAutoMigrate toggles schema-application-on-deploy. Default true.
// Tests use this to disable migration when running without a project DB.
func (h *FunctionHandler) SetAutoMigrate(enabled bool) {
	h.autoMigrate = enabled
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
	if shared, ok := h.clients[sharedClientKey]; ok && h.k8sClient == nil {
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
	if err := h.k8sClient.EnsureDenoRuntime(ctx, namespace, h.denoRuntimeSpecFor(projectID)); err != nil {
		return nil, fmt.Errorf("ensure deno runtime: %w", err)
	}
	// First-deploy race: EnsureDenoRuntime creates the Deployment but the pod
	// may not be ready yet. If we proceed immediately to Deploy, the HTTP call
	// fails with connection refused / 502. Wait for the pod to become ready.
	if err := h.waitForDenoReady(ctx, namespace); err != nil {
		return nil, fmt.Errorf("deno runtime did not become ready: %w", err)
	}

	h.clientMu.Lock()
	client := h.newProjectClient(projectID, namespace)
	h.clients[projectID] = client
	h.clientMu.Unlock()
	return client, nil
}

// newProjectClient builds a client for the project's in-namespace runtime
// service, authenticated with the project-derived secret (SEC-C5).
func (h *FunctionHandler) newProjectClient(projectID, namespace string) *edgefn.RuntimeClient {
	var url string
	if h.runtimeURLFn != nil {
		url = h.runtimeURLFn(namespace)
	} else {
		url = fmt.Sprintf("http://deno-runtime.%s.svc.cluster.local:8000", namespace)
	}
	return edgefn.NewRuntimeClient(url, edgefn.DeriveRuntimeSecret(h.runtimeSecret, projectID))
}

// waitForDenoReady polls the Deno runtime pod's readiness until it's up or
// the deadline is reached. Uses the k8s client's IsPodReady which already
// knows about pod phase + readiness conditions. 60s total budget is enough
// for image pull (first time) + container start.
//
// Skipped entirely if runtimeURLFn is set (integration tests point at
// pre-started subprocesses) or if k8sClient is nil (legacy shared mode).
func (h *FunctionHandler) waitForDenoReady(ctx context.Context, namespace string) error {
	if h.runtimeURLFn != nil || h.k8sClient == nil {
		return nil
	}
	// Deno pod is deployed with label app=deno-runtime. We poll GetPods and check
	// any pod whose name starts with "deno-runtime". 120s covers first-time image
	// pull + container start in fresh minikube/kind clusters.
	deadline := time.Now().Add(120 * time.Second)
	for time.Now().Before(deadline) {
		if h.isDenoPodReady(ctx, namespace) {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
	return fmt.Errorf("deno runtime pod in namespace %s did not become ready within 60s", namespace)
}

// isDenoPodReady returns true if at least one deno-runtime pod in namespace is ready.
func (h *FunctionHandler) isDenoPodReady(ctx context.Context, namespace string) bool {
	pods, err := h.k8sClient.GetPods(ctx, namespace, "app=deno-runtime")
	if err != nil {
		return false
	}
	for _, p := range pods {
		if strings.HasPrefix(p.Name, "deno-runtime") {
			if ready, perr := h.k8sClient.IsPodReady(ctx, namespace, p.Name); perr == nil && ready {
				return true
			}
		}
	}
	return false
}

// tierFor looks up the project's tier from the instance store. Falls back to
// "FREE" when the instance has no tier recorded. Used to size the Deno
// runtime pod's CPU/memory requests + limits.
func (h *FunctionHandler) tierFor(projectID string) string {
	if h.instanceStore == nil {
		return "FREE"
	}
	inst, err := h.instanceStore.FindByProjectID(projectID)
	if err != nil || inst == nil {
		return "FREE"
	}
	if inst.Tier == "" {
		return "FREE"
	}
	return string(inst.Tier)
}

// namespaceFor returns the K8s namespace recorded on the project's instance
// row. Returns "" if the instance is unknown or has no namespace — callers
// must treat empty as "cannot deploy" rather than computing a guess. The
// previous {orgID}-{projectId} fallback could land deploys in the wrong
// namespace because real CNPG namespaces use slug-based naming.
func (h *FunctionHandler) namespaceFor(projectID string) string {
	if h.instanceStore == nil {
		return ""
	}
	inst, err := h.instanceStore.FindByProjectID(projectID)
	if err != nil || inst == nil {
		return ""
	}
	return inst.Namespace
}

// SetRateLimit configures the public invoke rate limit (per project).
//
//	burst:           initial bucket size — how many requests can be made instantly
//	refillPerSecond: refill rate (≈ steady-state requests per second)
//
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

// orgSlugFor resolves the org slug for the EXCALIBASE_ORG_SLUG env var.
// Returns ("", false) if the slug can't be determined — caller decides
// whether to omit the env var (preferred) or fail. Vault paths no longer
// depend on this; this is purely for surfacing the slug to user code.
func (h *FunctionHandler) orgSlugFor(ctx context.Context, projectID string) (string, bool) {
	if h.instanceStore == nil || h.orgStore == nil {
		return "", false
	}
	inst, err := h.instanceStore.FindByProjectID(projectID)
	if err != nil || inst == nil || inst.OrgID == "" {
		return "", false
	}
	org, err := h.orgStore.FindOrgByID(ctx, inst.OrgID)
	if err != nil || org == nil {
		return "", false
	}
	return org.Slug, true
}

// builtinEnv returns platform-injected env vars for a function deploy.
// These override any user secret with the same key.
//
// Injected vars (all prefixed EXCALIBASE_):
//   - URL          — function's public invoke base (for calling sibling fns)
//   - PROJECT_ID   — the opaque project ref
//   - ORG_SLUG     — the owning org's slug, omitted if not resolvable
//   - DB_URL       — postgres DSN for the project's database (excalibase_app
//     role). Sourced from vault at projects/{projectId}/credentials/excalibase_app.
//     Absent if vault is sealed or the role doesn't exist.
//   - ANON_KEY     — JWT for the anon role from
//     projects/{projectId}/credentials/jwt_keys/anon_token.
//   - SERVICE_KEY  — JWT for the service role (bypasses RLS) from
//     projects/{projectId}/credentials/jwt_keys/service_token.
func (h *FunctionHandler) builtinEnv(ctx context.Context, projectID string) map[string]string {
	base := h.publicBaseURL
	if base == "" {
		base = "https://api.excalibase.io"
	}
	env := map[string]string{
		"EXCALIBASE_URL":        fmt.Sprintf("%s/functions/v1/%s", base, projectID),
		"EXCALIBASE_PROJECT_ID": projectID,
	}
	if slug, ok := h.orgSlugFor(ctx, projectID); ok {
		env["EXCALIBASE_ORG_SLUG"] = slug
	}

	// DB_URL — build from vault-stored app credentials if available.
	if h.secrets != nil {
		maps.Copy(env, h.buildDBEnv(projectID))
	}

	// ANON_KEY + SERVICE_KEY — fetched from vault where the auth service
	// publishes them. Tolerant: absent keys don't block deploy, function just
	// can't authenticate back to sibling services until the keys exist.
	if h.vault != nil {
		if anon := h.readVaultString(fmt.Sprintf("projects/%s/credentials/jwt_keys/anon_token", projectID)); anon != "" {
			env["EXCALIBASE_ANON_KEY"] = anon
		}
		if svc := h.readVaultString(fmt.Sprintf("projects/%s/credentials/jwt_keys/service_token", projectID)); svc != "" {
			env["EXCALIBASE_SERVICE_KEY"] = svc
		}
	}

	return env
}

// buildDBEnv returns EXCALIBASE_DB_URL for a project with app credentials in
// vault. Missing credentials yield no entries.
func (h *FunctionHandler) buildDBEnv(projectID string) map[string]string {
	if h.vault == nil {
		return nil
	}
	path := fmt.Sprintf("projects/%s/credentials/excalibase_app", projectID)
	creds, err := h.vault.Get(path)
	if err != nil || creds == nil {
		return nil
	}
	target := dbTarget{
		host: creds["host"], port: creds["port"],
		user: creds["username"], pass: creds["password"], db: creds["database"],
	}
	if target.host == "" || target.user == "" || target.db == "" {
		return nil
	}
	if target.port == "" {
		target.port = "5432"
	}
	return map[string]string{"EXCALIBASE_DB_URL": target.url(target.host)}
}

// dbTarget is the vault-stored app credential set a deploy DSN is built from.
type dbTarget struct {
	host, port, user, pass, db string
}

// url renders the DSN. Intentionally sslmode=require — managed project
// databases terminate TLS inside the cluster.
func (t dbTarget) url(host string) string {
	return fmt.Sprintf("postgres://%s:%s@%s/%s?sslmode=require",
		t.user, t.pass, net.JoinHostPort(host, t.port), t.db)
}

// readVaultString reads a vault path and returns a single string value. The
// vault stores values as map[string]string; we pick the first non-empty one
// or a value keyed "token" / "value" if present.
func (h *FunctionHandler) readVaultString(path string) string {
	if h.vault == nil {
		return ""
	}
	data, err := h.vault.Get(path)
	if err != nil || data == nil {
		return ""
	}
	for _, k := range []string{"token", "value", "key"} {
		if v, ok := data[k]; ok && v != "" {
			return v
		}
	}
	for _, v := range data {
		if v != "" {
			return v
		}
	}
	return ""
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
	tenant, _ := custommw.TenantIDFromContext(r.Context())
	log.Printf("tenant=%s action=fn_create path=%s", tenant, r.URL.Path)

	r.Body = http.MaxBytesReader(w, r.Body, int64(edgefn.MaxCodeSize+16*1024))
	var fn edgefn.Function
	if err := json.NewDecoder(r.Body).Decode(&fn); err != nil {
		httpError(w, errInvalidRequestBody, http.StatusBadRequest)
		return
	}
	fn.ProjectID = projectID
	fn.Active = true

	if err := h.store.Save(&fn); err != nil {
		httpError(w, safeError(err), http.StatusBadRequest)
		return
	}

	// Bundle + deploy to runtime with merged secrets.
	code, err := fn.BundleWith(h.sharedFilesFor(fn.ProjectID))
	if err != nil {
		httpError(w, safeError(err), http.StatusInternalServerError)
		return
	}

	// Extract user-declared schema (if any) and apply it before deploying
	// the function bundle. If migration fails we roll back the store record
	// so the deploy is atomic.
	if !h.applyExtractedSchema(w, r, projectID, code, &fn) {
		return
	}

	// Phase 8.5: sync the bundle's cron registry to excalibase.excalibase_cron_jobs
	// in a transaction that we hold open across the runtime deploy. On
	// deploy failure we rollback so the table never drifts ahead of the
	// runtime. On deploy success we commit, making the new schedule
	// visible to the CronRunner on its next tick.
	cronTx, cronCommit, cronErr := h.beginCronSync(r.Context(), projectID, fn.ID, fn.CronJobs)
	if cronErr != nil {
		_ = h.store.Delete(projectID, fn.ID)
		httpError(w, "cron sync failed: "+safeError(cronErr), http.StatusBadGateway)
		return
	}

	env := h.createEnv(r.Context(), projectID, fn.ID)

	client, err := h.runtimeClientFor(r.Context(), projectID)
	if err != nil {
		_ = rollbackCronSync(cronTx)
		_ = h.store.Delete(projectID, fn.ID)
		httpError(w, "runtime unavailable: "+safeError(err), http.StatusServiceUnavailable)
		return
	}
	deployErr := client.Deploy(r.Context(), deployRequestFor(&fn, code, env, h.effectiveEgressHosts(projectID)))
	if deployErr != nil {
		// Rollback the cron sync alongside the store record — the deploy
		// didn't land, so we shouldn't keep stale crons (or a stale
		// function) around.
		_ = rollbackCronSync(cronTx)
		if delErr := h.store.Delete(projectID, fn.ID); delErr != nil {
			log.Printf("WARN: rollback function store: %v", delErr)
		}
		httpError(w, "failed to deploy: "+safeError(deployErr), http.StatusBadGateway)
		return
	}
	// Phase 8.5: deploy succeeded — commit the cron sync so the cron
	// registry table reflects the latest bundle.
	if commitErr := cronCommit(); commitErr != nil {
		log.Printf("WARN: commit cron sync for %s/%s: %v", projectID, fn.ID, commitErr)
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(fn)
}

// createEnv builds the env for a first deploy. Unavailable user secrets
// degrade to builtins only.
func (h *FunctionHandler) createEnv(ctx context.Context, projectID, fnID string) map[string]string {
	builtins := h.builtinEnv(ctx, projectID)
	env, err := h.mergeSecrets(projectID, builtins)
	if err != nil {
		log.Printf("WARN: build env for %s/%s: %v", projectID, fnID, err)
		return builtins
	}
	return env
}

// deployEnv merges the project's user secrets over the platform builtins.
// A secrets failure is an error: replay and redeploy must not ship a
// function without its secrets.
func (h *FunctionHandler) deployEnv(ctx context.Context, projectID string) (map[string]string, error) {
	return h.mergeSecrets(projectID, h.builtinEnv(ctx, projectID))
}

func (h *FunctionHandler) mergeSecrets(projectID string, builtins map[string]string) (map[string]string, error) {
	if h.secrets == nil {
		return builtins, nil
	}
	return h.secrets.BuildEnvForDeploy(projectID, builtins)
}

// deployRequestFor is the single place a runtime deploy payload is assembled,
// so first deploy, secret-triggered redeploy and cold-start replay all ship
// byte-identical bundles.
//
// Phase 5b: the function's captured SchemaJSON is injected as a preamble so
// the worker-side schema helper (`runtime/schema.ts`) can pre-flight
// collection access and search/vector index lookup. Bundles without a
// defineSchema call get no preamble — the runtime falls back to permissive
// mode. json.RawMessage is verbatim JSON, so embedding it as a JS object
// literal is safe: the source was JSON-marshalled by ExtractSchema and the
// slot name matches what deno-server/runtime/schema.ts reads.
//
// allowedHosts is the project's effective egress allowlist (EXC-348); the
// runtime grants the worker `net` access to exactly these hosts.
func deployRequestFor(fn *edgefn.Function, code string, env map[string]string, allowedHosts []string) edgefn.DeployRequest {
	if len(fn.SchemaJSON) > 0 {
		code = "globalThis.__excalibase_function_metadata = { schemaJson: " +
			string(fn.SchemaJSON) + " };\n" + code
	}
	return edgefn.DeployRequest{ID: fn.RuntimeID(), Code: code, Secrets: env, AllowedHosts: allowedHosts}
}

// applyExtractedSchema extracts the user-declared schema from the bundled code,
// persists it on the function record, and (when auto-migrate is on) applies it
// to the project DB. On migration failure it rolls back the store record and
// writes an HTTP error. Returns false when the caller should stop (an error
// response has already been written); true when the deploy may continue.
// Schema extraction is a no-op for bundles that don't call defineSchema.
func (h *FunctionHandler) applyExtractedSchema(w http.ResponseWriter, r *http.Request, projectID, code string, fn *edgefn.Function) bool {
	schema, found, sErr := edgefn.ExtractSchema(code)
	if sErr != nil {
		log.Printf("WARN: schema extraction failed for %s/%s: %v", projectID, fn.ID, sErr)
		return true
	}
	if !found {
		return true
	}
	if raw, mErr := json.Marshal(schema); mErr == nil {
		fn.SchemaJSON = raw
		// Re-save to persist SchemaJSON before migration runs.
		if err := h.store.Save(fn); err != nil {
			log.Printf("WARN: save schema for %s/%s: %v", projectID, fn.ID, err)
		}
	}
	if !h.autoMigrate || h.projectDBFn == nil {
		return true
	}
	db, dbErr := h.projectDBFn(r.Context(), projectID)
	if dbErr != nil {
		_ = h.store.Delete(projectID, fn.ID)
		httpError(w, "failed to open project db for migration: "+safeError(dbErr), http.StatusBadGateway)
		return false
	}
	if err := edgefn.ApplySchema(r.Context(), db, projectID, schema); err != nil {
		_ = h.store.Delete(projectID, fn.ID)
		httpError(w, "schema migration failed: "+safeError(err), http.StatusBadGateway)
		return false
	}
	return true
}

// ApplySchemaFromStore applies the stored SchemaJSON for every function in
// the project. Wired at POST /api/projects/{projectId}/schema/apply for
// deployments running with EXCALIBASE_AUTO_MIGRATE=false. Idempotent — the
// migrator only adds tables/indexes/columns, never drops them.
func (h *FunctionHandler) ApplySchemaFromStore(w http.ResponseWriter, r *http.Request) {
	projectID := chi.URLParam(r, "projectId")
	if err := edgefn.ValidateProjectID(projectID); err != nil {
		httpError(w, safeError(err), http.StatusBadRequest)
		return
	}
	if h.projectDBFn == nil {
		httpError(w, "schema apply not configured (no project db resolver)", http.StatusServiceUnavailable)
		return
	}
	fns, err := h.store.List(projectID)
	if err != nil {
		httpError(w, safeError(err), http.StatusInternalServerError)
		return
	}
	db, err := h.projectDBFn(r.Context(), projectID)
	if err != nil {
		httpError(w, "open project db: "+safeError(err), http.StatusBadGateway)
		return
	}
	applied := 0
	for _, fn := range fns {
		if len(fn.SchemaJSON) == 0 {
			continue
		}
		var schema edgefn.Schema
		if err := json.Unmarshal(fn.SchemaJSON, &schema); err != nil {
			log.Printf("WARN: parse schema for %s/%s: %v", projectID, fn.ID, err)
			continue
		}
		if err := edgefn.ApplySchema(r.Context(), db, projectID, schema); err != nil {
			httpError(w, "apply schema for "+fn.ID+": "+safeError(err), http.StatusBadGateway)
			return
		}
		applied++
	}
	writeJSON(w, map[string]any{"status": "applied", "functions": applied})
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
		httpError(w, errFunctionNotFound, http.StatusNotFound)
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
		httpError(w, errFunctionNotFound, http.StatusNotFound)
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

// Logs returns the recent user-code log ring buffer for a function. Supports
// incremental polling via ?since=<unix-ms>. Logs are in-memory only — they
// reset on runtime pod restart, and callers should not rely on them for
// persistence. This is the endpoint the Studio logs panel polls.
func (h *FunctionHandler) Logs(w http.ResponseWriter, r *http.Request) {
	projectID := chi.URLParam(r, "projectId")
	fnID := chi.URLParam(r, "fnId")
	fn, err := h.store.Get(projectID, fnID)
	if err != nil {
		httpError(w, safeError(err), http.StatusBadRequest)
		return
	}
	if fn == nil {
		httpError(w, errFunctionNotFound, http.StatusNotFound)
		return
	}

	var sinceMs int64
	if s := r.URL.Query().Get("since"); s != "" {
		if n, perr := strconv.ParseInt(s, 10, 64); perr == nil && n > 0 {
			sinceMs = n
		}
	}

	client, cerr := h.runtimeClientFor(r.Context(), projectID)
	if cerr != nil {
		// Runtime not ready yet — return empty list rather than error so the
		// Studio panel can poll peacefully until the pod comes up.
		writeJSON(w, map[string]interface{}{"logs": []edgefn.LogEntry{}})
		return
	}
	logs, err := client.Logs(r.Context(), fn.RuntimeID(), sinceMs)
	if err != nil {
		if errors.Is(err, edgefn.ErrLogsNotFound) {
			writeJSON(w, map[string]interface{}{"logs": []edgefn.LogEntry{}})
			return
		}
		httpError(w, safeError(err), http.StatusBadGateway)
		return
	}
	if logs == nil {
		logs = []edgefn.LogEntry{}
	}
	writeJSON(w, map[string]interface{}{"logs": logs})
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
		httpError(w, errFunctionNotFound, http.StatusNotFound)
		return
	}
	h.forwardToRuntime(w, r, fn, true /* stripAuth */)
}

// jwkCacheTTL controls how long the public key is cached before we re-fetch
// it from vault. 5 minutes is a reasonable balance — key rotation is rare,
// and a stale key for 5 minutes during rotation is survivable.
const jwkCacheTTL = 5 * time.Minute

// loadSigningPublicKey fetches and caches the platform-wide ES256 public key
// used by the auth service to sign project JWTs. Vault path: pki/signing/public
// (PEM-encoded, same shape as vault.go:InitPKI writes).
func (h *FunctionHandler) loadSigningPublicKey() (*ecdsa.PublicKey, error) {
	h.jwkMu.RLock()
	if h.jwkKey != nil && time.Since(h.jwkLoaded) < jwkCacheTTL {
		key := h.jwkKey
		h.jwkMu.RUnlock()
		return key, nil
	}
	h.jwkMu.RUnlock()

	h.jwkMu.Lock()
	defer h.jwkMu.Unlock()
	// Re-check in case another goroutine refreshed it while we were waiting.
	if h.jwkKey != nil && time.Since(h.jwkLoaded) < jwkCacheTTL {
		return h.jwkKey, nil
	}

	if h.vault == nil {
		return nil, errors.New("vault not configured — cannot fetch signing key")
	}
	data, err := h.vault.Get("pki/signing/public")
	if err != nil {
		return nil, fmt.Errorf("fetch public key from vault: %w", err)
	}
	pemStr, ok := data["key"]
	if !ok || pemStr == "" {
		return nil, errors.New("pki/signing/public missing 'key' field")
	}
	block, _ := pem.Decode([]byte(pemStr))
	if block == nil {
		return nil, errors.New("invalid PEM in pki/signing/public")
	}
	pub, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parse ec public key: %w", err)
	}
	ecPub, ok := pub.(*ecdsa.PublicKey)
	if !ok {
		return nil, fmt.Errorf("public key is not ECDSA (got %T)", pub)
	}
	h.jwkKey = ecPub
	h.jwkLoaded = time.Now()
	return ecPub, nil
}

// validateProjectJWT verifies an ES256 signature and binds the token to a
// specific project. Returns the JWT scope (`authenticated`, `public`,
// `service`, or empty for legacy password-flow tokens) so callers can apply
// per-function policy if they choose.
//
// Verification gates:
//  1. Algorithm is ES256 (rejects alg=none and HMAC-confusion).
//  2. Signature verifies against the platform-wide PKI public key (vault-published).
//  3. exp/nbf/iat valid (handled by jwt.Parse via token.Valid).
//  4. claims["projectId"] EXACTLY matches the URL's expected project ID.
//     This is what prevents a JWT minted for project A from being replayed
//     against project B's function URL — the auth service's `iss` claim is a
//     hardcoded platform identifier (`excalibase`) that contains no project
//     binding, so `projectId` is the only authoritative tenant marker.
//  5. (Optional) iss matches the configured expected issuer if set via env
//     EXCALIBASE_AUTH_ISS — defense in depth that the token came from the
//     platform's auth service, not some other ES256 signer.
func (h *FunctionHandler) validateProjectJWT(tokenStr, expectedProjectID string) (string, error) {
	key, err := h.loadSigningPublicKey()
	if err != nil {
		return "", fmt.Errorf("load signing key: %w", err)
	}

	token, err := jwt.Parse(tokenStr, func(t *jwt.Token) (interface{}, error) {
		if _, ok := t.Method.(*jwt.SigningMethodECDSA); !ok {
			return nil, fmt.Errorf("unexpected signing method %v", t.Header["alg"])
		}
		return key, nil
	}, jwt.WithValidMethods([]string{"ES256"}))
	if err != nil {
		return "", fmt.Errorf("parse/verify jwt: %w", err)
	}
	if !token.Valid {
		return "", errors.New("jwt is not valid")
	}

	claims, ok := token.Claims.(jwt.MapClaims)
	if !ok {
		return "", errors.New("jwt claims are not a map")
	}

	tokenProjectID, ok := claims["projectId"].(string)
	if !ok || tokenProjectID == "" {
		return "", errors.New("jwt missing projectId claim")
	}
	if tokenProjectID != expectedProjectID {
		return "", fmt.Errorf("jwt projectId %q does not match url %q", tokenProjectID, expectedProjectID)
	}

	if expectedIss := h.expectedJWTIssuer; expectedIss != "" {
		iss, _ := claims["iss"].(string)
		if iss != expectedIss {
			return "", fmt.Errorf("jwt iss %q does not match expected %q", iss, expectedIss)
		}
	}

	if err := h.checkTokenUse(claims); err != nil {
		return "", err
	}
	if err := h.checkAudience(claims, expectedProjectID); err != nil {
		return "", err
	}

	scope, _ := claims["scope"].(string)
	return scope, nil
}

// Machine-readable rejection codes surfaced in the 401 body so an operator can
// tell a stale-token rollout problem from a credential-type mix-up.
const (
	errCodeAudMismatch        = "aud_mismatch"
	errCodeRefreshNotAccepted = "refresh_token_not_accepted"
	// tokenUseRefresh is the token_use value the auth service stamps on
	// refresh credentials. They are exchanged at the auth service's token
	// endpoint, never presented to a project API.
	tokenUseRefresh = "refresh"
)

// codedJWTError carries a stable error code alongside a human-readable reason.
// enforceJWT surfaces the code (and only the code) to the caller.
type codedJWTError struct {
	code   string
	reason string
}

func (e *codedJWTError) Error() string { return e.code + ": " + e.reason }

// Code returns the stable machine-readable rejection code.
func (e *codedJWTError) Code() string { return e.code }

// checkTokenUse refuses refresh credentials on API calls. A missing token_use
// claim is accepted — legacy access tokens predate the claim.
func (h *FunctionHandler) checkTokenUse(claims jwt.MapClaims) error {
	if use, _ := claims["token_use"].(string); use == tokenUseRefresh {
		return &codedJWTError{code: errCodeRefreshNotAccepted, reason: "refresh credentials are not API access tokens"}
	}
	return nil
}

// checkAudience enforces that the token was minted for THIS project: its aud
// claim must contain audPrefix+projectId. Disabled (accept anything) when
// requireAud is false, which exists purely for a phased rollout.
func (h *FunctionHandler) checkAudience(claims jwt.MapClaims, expectedProjectID string) error {
	if !h.requireAud {
		return nil
	}
	want := h.audPrefix + expectedProjectID
	for _, got := range normalizeAudience(claims["aud"]) {
		if got == want {
			return nil
		}
	}
	return &codedJWTError{code: errCodeAudMismatch, reason: "jwt aud does not contain the project audience"}
}

// normalizeAudience flattens the two shapes RFC 7519 allows for aud — a single
// string, or an array of strings — into a slice. Empty strings and non-string
// array members are dropped; anything else yields nil.
func normalizeAudience(raw interface{}) []string {
	switch v := raw.(type) {
	case string:
		if v == "" {
			return nil
		}
		return []string{v}
	case []string:
		return v
	case []interface{}:
		out := make([]string, 0, len(v))
		for _, item := range v {
			if s, ok := item.(string); ok && s != "" {
				out = append(out, s)
			}
		}
		return out
	default:
		return nil
	}
}

// writeCORSHeaders sets permissive CORS headers for edge functions. Edge
// functions are designed to be called from any origin (browser, mobile,
// server-to-server) so `*` is the right default. Users can tighten this
// per-project later.
func writeCORSHeaders(w http.ResponseWriter) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, PATCH, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type, apikey, x-client-info, x-excalibase-auth")
	w.Header().Set("Access-Control-Max-Age", "86400")
}

// PublicInvoke is the Supabase-style public route /functions/v1/{projectId}/{fnId}.
// Enforces:
//  1. CORS — preflight + permissive headers for browser callers
//  2. Per-project rate limit (returns 429 when exceeded)
//  3. JWT presence check if the function has VerifyJwt enabled (default true)
//  4. Forwards Authorization header to the function so user code can inspect it
func (h *FunctionHandler) PublicInvoke(w http.ResponseWriter, r *http.Request) {
	writeCORSHeaders(w)
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusNoContent)
		return
	}

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
		httpError(w, errFunctionNotFound, http.StatusNotFound)
		return
	}

	// Phase 7: internal-only functions are NOT reachable via the public
	// route. Return the regular 404 phrasing so an attacker can't tell
	// "internal-only" apart from "missing". Internal callers (sibling
	// functions via ctx.runX, admin tools) reach the handler via
	// /internal/invoke/{projectId}/{fnId} instead.
	if fn.IsInternal {
		httpError(w, errFunctionNotFound, http.StatusNotFound)
		return
	}

	if fn.JwtVerificationRequired() {
		if !h.enforceJWT(w, r, projectID) {
			return
		}
	}

	h.forwardToRuntime(w, r, fn, false /* keep Authorization */)
}

// enforceJWT validates the Authorization Bearer token and sets X-Excalibase-Scope.
// Returns false (and writes the error response) if validation fails.
func (h *FunctionHandler) enforceJWT(w http.ResponseWriter, r *http.Request, projectID string) bool {
	authHeader := r.Header.Get("Authorization")
	if authHeader == "" || !strings.HasPrefix(authHeader, "Bearer ") {
		httpError(w, "missing or invalid Authorization header", http.StatusUnauthorized)
		return false
	}
	token := strings.TrimPrefix(authHeader, "Bearer ")
	scope, err := h.validateProjectJWT(token, projectID)
	if err != nil {
		if isVaultUnavailableError(err) {
			log.Printf("ERROR: jwt verify unavailable for %s: %v", projectID, err)
			httpError(w, "auth temporarily unavailable", http.StatusServiceUnavailable)
			return false
		}
		// A coded rejection answers with the bare code so operators can
		// distinguish an audience-rollout failure from a bad signature.
		var coded *codedJWTError
		if errors.As(err, &coded) {
			httpError(w, coded.Code(), http.StatusUnauthorized)
			return false
		}
		httpError(w, "invalid jwt: "+safeError(err), http.StatusUnauthorized)
		return false
	}
	if scope != "" {
		r.Header.Set("X-Excalibase-Scope", scope)
	}
	return true
}

// isVaultUnavailableError returns true for errors that indicate the vault key
// cannot be fetched (temporarily, not a bad-token situation).
func isVaultUnavailableError(err error) bool {
	msg := err.Error()
	return strings.Contains(msg, "vault not configured") ||
		strings.Contains(msg, "missing 'key' field") ||
		strings.Contains(msg, "fetch public key from vault")
}

// parseContentLength returns the request's Content-Length as an int64 if
// present and valid. Avoids importing strconv into the hot path.
func parseContentLength(v string) (int64, error) {
	var n int64
	for i := 0; i < len(v); i++ {
		c := v[i]
		if c < '0' || c > '9' {
			return 0, fmt.Errorf("invalid content-length %q", v)
		}
		n = n*10 + int64(c-'0')
		if n < 0 {
			return 0, fmt.Errorf("content-length overflow")
		}
	}
	return n, nil
}

// maxInvokeBodyBytes caps the request body forwarded to the runtime (1 MB).
// Matches the Deno runtime's MAX_INVOKE_BODY; the Deno runtime will also
// refuse larger payloads.
const maxInvokeBodyBytes = 1024 * 1024

// forwardToRuntime serializes the incoming HTTP request and ships it to the
// Deno runtime via the RuntimeClient. The runtime's response is written back
// to w with status, headers, and body intact.
func (h *FunctionHandler) forwardToRuntime(w http.ResponseWriter, r *http.Request, fn *edgefn.Function, stripAuth bool) {
	// Refuse oversized requests upfront via Content-Length before reading
	// the body into memory. Protects against naive DoS via giant POSTs.
	if cl := r.Header.Get("Content-Length"); cl != "" {
		if n, perr := parseContentLength(cl); perr == nil && n > maxInvokeBodyBytes {
			httpError(w, "payload too large", http.StatusRequestEntityTooLarge)
			return
		}
	}
	// Defense-in-depth: MaxBytesReader enforces the cap for chunked
	// transfer-encoded requests that lack Content-Length.
	r.Body = http.MaxBytesReader(w, r.Body, maxInvokeBodyBytes)
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
	keys, err := h.secrets.ListKeys(projectID)
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
		httpError(w, errInvalidRequestBody, http.StatusBadRequest)
		return
	}
	if err := h.secrets.Set(projectID, body.Key, body.Value); err != nil {
		httpError(w, safeError(err), http.StatusBadRequest)
		return
	}
	h.redeployAll(r, projectID)
	writeJSON(w, map[string]string{"status": "set", "key": body.Key})
}

// DeleteSecret removes a secret and redeploys the project's functions.
func (h *FunctionHandler) DeleteSecret(w http.ResponseWriter, r *http.Request) {
	projectID := chi.URLParam(r, "projectId")
	key := chi.URLParam(r, "key")
	if err := h.secrets.Delete(projectID, key); err != nil {
		httpError(w, safeError(err), http.StatusBadRequest)
		return
	}
	h.redeployAll(r, projectID)
	writeJSON(w, map[string]string{"status": "deleted", "key": key})
}

// redeployAll re-pushes every function of the project to the runtime so the
// latest env (user secrets + builtins) is picked up. Errors are logged and
// not surfaced — secret write succeeded, so the admin UI should still 200.
func (h *FunctionHandler) redeployAll(r *http.Request, projectID string) {
	list, err := h.store.List(projectID)
	if err != nil || len(list) == 0 {
		return
	}
	env, err := h.deployEnv(r.Context(), projectID)
	if err != nil {
		log.Printf("WARN: redeploy build env: %v", err)
		return
	}
	client, cerr := h.runtimeClientFor(r.Context(), projectID)
	if cerr != nil {
		log.Printf("WARN: redeploy runtime client: %v", cerr)
		return
	}
	shared := h.sharedFilesFor(projectID)
	allowedHosts := h.effectiveEgressHosts(projectID)
	for _, fn := range list {
		code, err := fn.BundleWith(shared)
		if err != nil {
			log.Printf("WARN: redeploy bundle %s: %v", fn.ID, err)
			continue
		}
		if err := client.Deploy(r.Context(), deployRequestFor(fn, code, env, allowedHosts)); err != nil {
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

// --- Phase 2: export-metadata endpoints (SDK codegen + runtime callback) ---

// runtimeTokenHeader names the shared-secret header used on the internal
// /metadata callback from the Deno runtime back to provisioning. The same
// runtimeSecret that authenticates deploy/invoke RPCs authenticates this.
const runtimeTokenHeader = "X-Excalibase-Runtime-Token"

// authorizeRuntimeToken gates the internal runtime-only routes. It FAILS
// CLOSED: if no runtimeSecret is configured the route is rejected with 503
// rather than letting unauthenticated callers through. When a secret is
// configured, the request must present an exactly-matching token header.
// Returns true when the request is authorized to proceed.
func (h *FunctionHandler) authorizeRuntimeToken(w http.ResponseWriter, r *http.Request, projectID string) bool {
	if h.runtimeSecret == "" {
		httpError(w, "internal route not configured", http.StatusServiceUnavailable)
		return false
	}
	// The presented token must match this project's derived secret — a token
	// minted for another project does not authenticate here (SEC-C5).
	expected := edgefn.DeriveRuntimeSecret(h.runtimeSecret, projectID)
	if subtle.ConstantTimeCompare([]byte(r.Header.Get(runtimeTokenHeader)), []byte(expected)) != 1 {
		httpError(w, "unauthorized", http.StatusUnauthorized)
		return false
	}
	return true
}

// exportMetadataEntry is the on-wire shape of one element returned by
// ListExportMetadata. Mirrors what the SDK codegen reads:
//
//	[{ id, name, runtimeShape, exports: [...], lastDeployedAt }]
//
// `Exports` is left as a json.RawMessage so the runtime's reported payload
// passes through verbatim — no re-parse + re-serialize on every read.
type exportMetadataEntry struct {
	ID             string          `json:"id"`
	Name           string          `json:"name"`
	RuntimeShape   string          `json:"runtimeShape"`
	Exports        json.RawMessage `json:"exports"`
	LastDeployedAt time.Time       `json:"lastDeployedAt"`
}

// ListExportMetadata serves
// GET /api/projects/{projectId}/functions/_metadata — the SDK codegen
// entry point. Returns every persisted function in the project with its
// runtime-reported export metadata. Functions that haven't yet had a
// callback (or that ship a v1 Fetch handler) get `exports: []` so the
// codegen output is shape-stable.
func (h *FunctionHandler) ListExportMetadata(w http.ResponseWriter, r *http.Request) {
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
	out := make([]exportMetadataEntry, 0, len(list))
	for _, fn := range list {
		exports := fn.ExportMetadata
		if len(exports) == 0 {
			exports = json.RawMessage("[]")
		}
		out = append(out, exportMetadataEntry{
			ID:             fn.ID,
			Name:           fn.Name,
			RuntimeShape:   fn.RuntimeShape,
			Exports:        exports,
			LastDeployedAt: fn.UpdatedAt,
		})
	}
	writeJSON(w, out)
}

// ReceiveExportMetadata serves
// POST /internal/runtime/functions/{fnId}/metadata — called by the Deno
// runtime's main thread when a worker reports its scanned exports. Auth:
// the same runtimeSecret shared between provisioning and runtime; required
// in the X-Excalibase-Runtime-Token header.
//
// Body: { "projectId": "...", "exports": [...] }. The exports array is
// stored verbatim on the Function.ExportMetadata field so a later
// /_metadata read can return it untouched.
func (h *FunctionHandler) ReceiveExportMetadata(w http.ResponseWriter, r *http.Request) {
	fnID := chi.URLParam(r, "fnId")
	if err := edgefn.ValidateID(fnID); err != nil {
		httpError(w, safeError(err), http.StatusBadRequest)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, 256*1024)
	var body struct {
		ProjectID string          `json:"projectId"`
		Exports   json.RawMessage `json:"exports"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httpError(w, errInvalidRequestBody, http.StatusBadRequest)
		return
	}
	if err := edgefn.ValidateProjectID(body.ProjectID); err != nil {
		httpError(w, safeError(err), http.StatusBadRequest)
		return
	}
	// Authenticate against the project the runtime claims to be — its token must
	// match that project's derived secret (SEC-C5).
	if !h.authorizeRuntimeToken(w, r, body.ProjectID) {
		return
	}
	if len(body.Exports) == 0 {
		// Tolerate empty bodies — record an empty array so /_metadata
		// returns a consistent shape.
		body.Exports = json.RawMessage("[]")
	}
	// Validate that exports parses as a JSON array — guards against the
	// runtime sending malformed payloads.
	var probe []interface{}
	if err := json.Unmarshal(body.Exports, &probe); err != nil {
		httpError(w, "exports must be a JSON array", http.StatusBadRequest)
		return
	}

	fn, err := h.store.Get(body.ProjectID, fnID)
	if err != nil {
		httpError(w, safeError(err), http.StatusBadRequest)
		return
	}
	if fn == nil {
		httpError(w, errFunctionNotFound, http.StatusNotFound)
		return
	}
	fn.ExportMetadata = body.Exports
	// Phase 7: surface the isInternal flag from the runtime-reported export
	// metadata onto the Function record so PublicInvoke can short-circuit
	// to 404 on the way in. We inspect the first entry's tag — Phase 2's
	// metadata contract is one entry per default export.
	if isInternalFromMetadata(body.Exports) {
		fn.IsInternal = true
	}
	if err := h.store.Save(fn); err != nil {
		httpError(w, safeError(err), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]string{"status": "updated", "id": fnID})
}

// isInternalFromMetadata returns true when the runtime-reported exports
// array tags the default export with `isInternal: true`. The metadata
// shape is `[{name, kind, argsJsonSchema, isInternal?}]`; we read only
// the first entry because Phase 2 contracts a single default export per
// function. Tolerates missing/empty payloads.
func isInternalFromMetadata(raw json.RawMessage) bool {
	if len(raw) == 0 {
		return false
	}
	var entries []map[string]interface{}
	if err := json.Unmarshal(raw, &entries); err != nil {
		return false
	}
	if len(entries) == 0 {
		return false
	}
	v, ok := entries[0]["isInternal"]
	if !ok {
		return false
	}
	b, _ := v.(bool)
	return b
}

// --- Phase 7: internal-invoke route + http* dispatch ---

// InternalInvoke serves POST /internal/invoke/{projectId}/{fnId} — the
// server-to-server bridge used by `ctx.runQuery / runMutation / runAction`
// when a function in one runtime targets a function in a different runtime
// (or, for now, in the same runtime via the gateway). Authenticated with
// the runtime-token shared secret — anything else 401s before the function
// is even looked up.
//
// The body forwards to the runtime verbatim, exactly like Invoke, but
// without the Authorization-strip behaviour (internal callers carry a
// runtime-token, not a user JWT).
func (h *FunctionHandler) InternalInvoke(w http.ResponseWriter, r *http.Request) {
	projectID := chi.URLParam(r, "projectId")
	fnID := chi.URLParam(r, "fnId")
	if err := edgefn.ValidateProjectID(projectID); err != nil {
		httpError(w, safeError(err), http.StatusBadRequest)
		return
	}
	if !h.authorizeRuntimeToken(w, r, projectID) {
		return
	}
	fn, err := h.store.Get(projectID, fnID)
	if err != nil {
		httpError(w, safeError(err), http.StatusBadRequest)
		return
	}
	if fn == nil {
		httpError(w, errFunctionNotFound, http.StatusNotFound)
		return
	}
	// Internal invoke reaches both public and internal-only functions —
	// no IsInternal short-circuit here. That's the whole point of the
	// route: it's the trusted path the runtime uses to compose calls.
	h.forwardToRuntime(w, r, fn, false /* keep auth headers — caller is the runtime */)
}

// PublicHttpInvoke serves /functions/v1/{projectId}/http/* — the entry
// point for httpAction and httpRouter functions. Differs from PublicInvoke
// in two ways:
//
//  1. The function is looked up by `Kind` (httpAction / httpRouter) rather
//     than by URL-supplied fnId. Phase 7 supports at most one httpAction
//     or one httpRouter per project (Convex parity); the first matching
//     Function wins.
//  2. For httpRouter, the request path is matched against the persisted
//     route table; a miss (or method mismatch) returns 404.
//
// Internal-only http* functions follow the same 404-on-public rule as the
// other internalX kinds — for consistency, even though Convex's
// `internalAction` doesn't have an http variant.
func (h *FunctionHandler) PublicHttpInvoke(w http.ResponseWriter, r *http.Request) {
	writeCORSHeaders(w)
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	projectID := chi.URLParam(r, "projectId")
	if err := edgefn.ValidateProjectID(projectID); err != nil {
		httpError(w, safeError(err), http.StatusBadRequest)
		return
	}
	if !h.allowProject(projectID) {
		w.Header().Set("Retry-After", "1")
		httpError(w, "rate limit exceeded", http.StatusTooManyRequests)
		return
	}

	fns, err := h.store.List(projectID)
	if err != nil {
		httpError(w, safeError(err), http.StatusInternalServerError)
		return
	}

	// Sub-path after the /http prefix — chi's `*` wildcard captures it.
	subPath := "/" + strings.TrimLeft(chi.URLParam(r, "*"), "/")

	for _, fn := range fns {
		if !httpFunctionMatches(fn, subPath, r.Method) {
			continue
		}
		if fn.JwtVerificationRequired() && !h.enforceJWT(w, r, projectID) {
			return
		}
		h.forwardToRuntime(w, r, fn, false)
		return
	}
	httpError(w, errFunctionNotFound, http.StatusNotFound)
}

// httpFunctionMatches reports whether the public-facing http* function fn should
// serve the request for subPath + method. Internal functions never match. An
// httpAction matches when subPath equals "/"+fn.ID; an httpRouter matches when
// its persisted route table contains the (path, method) pair.
func httpFunctionMatches(fn *edgefn.Function, subPath, method string) bool {
	if fn.IsInternal {
		return false
	}
	switch fn.Kind {
	case "httpAction":
		// The Go-side gateway treats `/functions/v1/{p}/http/<fnId>` as the
		// route for an httpAction whose fnId equals the segment.
		return subPath == "/"+fn.ID
	case "httpRouter":
		return matchRouterRoute(fn.HttpRoutes, subPath, method)
	default:
		return false
	}
}

// matchRouterRoute walks the persisted route table looking for a (path,
// method) match. Phase 7 keeps the matcher exact-only — no path parameters
// or wildcards. A method mismatch on an otherwise-known path returns
// `false` (and PublicHttpInvoke 404s) to match Convex's behaviour.
func matchRouterRoute(rawRoutes json.RawMessage, path, method string) bool {
	if len(rawRoutes) == 0 {
		return false
	}
	var routes []struct {
		Path   string `json:"path"`
		Method string `json:"method"`
	}
	if err := json.Unmarshal(rawRoutes, &routes); err != nil {
		return false
	}
	for _, route := range routes {
		if route.Path == path && route.Method == method {
			return true
		}
	}
	return false
}

// sharedFilesFor returns the project's shared modules (EXC-334) when the active
// store supports them. Best-effort: a lookup failure must not block a deploy, so
// it degrades to "no shared files" and the bundler reports any unresolved import.
func (h *FunctionHandler) sharedFilesFor(projectID string) []edgefn.File {
	provider, ok := h.store.(edgefn.SharedFileStore)
	if !ok {
		return nil
	}
	files, err := provider.SharedFiles(projectID)
	if err != nil {
		log.Printf("WARN: load shared files for %s: %v", projectID, err)
		return nil
	}
	return files
}
