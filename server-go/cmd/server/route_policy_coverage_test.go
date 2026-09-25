package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"database/sql"
	"encoding/pem"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/auth"
	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/docbrowser"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/edgefn"
	"github.com/excalibase/provisioning-poc/internal/handler"
	"github.com/excalibase/provisioning-poc/internal/k8s"
	custommw "github.com/excalibase/provisioning-poc/internal/middleware"
	"github.com/excalibase/provisioning-poc/internal/routepolicy"
	"github.com/excalibase/provisioning-poc/internal/testutil"
	"github.com/excalibase/provisioning-poc/internal/testutil/fakestore"
	"github.com/excalibase/provisioning-poc/internal/vaultclient"
	"github.com/excalibase/provisioning-poc/pkg/vault"
	"github.com/go-chi/chi/v5"
	"github.com/tus/tusd/v2/pkg/filestore"
	tusd "github.com/tus/tusd/v2/pkg/handler"
	"github.com/tus/tusd/v2/pkg/memorylocker"
)

// EXC-395: internal/routepolicy declares, for every route this server mounts,
// who may call it and what resolves their right to the resource in the path.
// This file is what keeps that table honest: it walks the REAL router built by
// buildRouter and fails when a mounted route has no row, when a row matches no
// mounted route, and when driving the router with synthetic principals
// contradicts what the row declares.
//
// Adding a route without adding a row breaks these tests.

const (
	policyViewerID   = "viewer-a"
	policyAdminID    = "orgadmin-a"
	policyOwnerID    = "owner-a"
	policyBoundID    = "bound-a"
	policySvcAuthID  = "svc-auth-policy"
	policySvcGqlID   = "svc-graphql-policy"
	policyOtherOrgID = "member-b"
)

// principal pairs a routepolicy.Principal with the bearer token that makes the
// real router see it that way.
type principal struct {
	routepolicy.Principal
	token string
}

// gateRefusalBodies is the complete set of bodies the authorization layers
// emit, matched exactly. Exactness is the point: a handler's own
// "project not found" 404 must never be read as a gate refusal, and a gate
// refusal must never be read as the handler answering.
var gateRefusalBodies = map[string]bool{
	`{"error":"authentication required"}`:                                true,
	`{"code":"token_expired","error":"token expired"}`:                   true,
	`{"error":"unauthenticated"}`:                                        true,
	`{"error":"project not found"}`:                                      true,
	`{"error":"insufficient project role"}`:                              true,
	`{"error":"insufficient permissions"}`:                               true,
	`{"error":"token lacks required scope: write"}`:                      true,
	`{"error":"this route requires a session or an unrestricted token"}`: true,
	`{"error":"token is not permitted to call this endpoint"}`:           true,
	// Refusals the handlers write themselves, in their own words.
	`{"error":"org not found","status":404}`:                 true,
	`{"error":"insufficient permissions","status":403}`:      true,
	`{"error":"only org owner can delete","status":403}`:     true,
	`{"error":"unauthorized","status":401}`:                  true,
	`{"error":"auth required","status":401}`:                 true,
	`{"error":"internal route not configured","status":503}`: true,
	// The end-user function gate: no studio credential is an end-user JWT.
	`{"error":"missing or invalid Authorization header","status":401}`: true,
}

// gateRefusalPrefixes are the refusals whose text names why the presented
// credential was rejected. Only enforceJWT writes this prefix, and it writes
// nothing else — the reason varies with the token, the refusal does not.
var gateRefusalPrefixes = []string{`{"error":"invalid jwt: `}

func isGateRefusal(body string) bool {
	body = strings.TrimSpace(body)
	if gateRefusalBodies[body] {
		return true
	}
	for _, prefix := range gateRefusalPrefixes {
		if strings.HasPrefix(body, prefix) {
			return true
		}
	}
	return false
}

// policyRouter builds the production router in cloud mode with every optional
// feature wired, so the walk sees the maximal mount rather than whichever
// subset a given deployment enables.
func policyRouter(t *testing.T) (*policyHarness, []principal) {
	t.Helper()
	instances := fakestore.NewInstances()
	seedProjects := func() {
		instances.Items[matrixProjectA] = &domain.DatabaseInstance{ProjectID: matrixProjectA, OrgID: matrixOrgA, Status: "ACTIVE"}
		instances.Items[matrixProjectB] = &domain.DatabaseInstance{ProjectID: matrixProjectB, OrgID: matrixOrgB, Status: "ACTIVE"}
	}
	seedProjects()

	platform := &fakePlatform{Orgs: fakestore.NewOrgs(), Tokens: fakestore.NewTokens()}
	platform.AddMember(matrixOrgA, policyViewerID, domain.OrgRoleViewer)
	platform.AddMember(matrixOrgA, matrixDevID, domain.OrgRoleDeveloper)
	platform.AddMember(matrixOrgA, policyAdminID, domain.OrgRoleAdmin)
	platform.AddMember(matrixOrgA, policyOwnerID, domain.OrgRoleOwner)
	platform.AddMember(matrixOrgA, policyBoundID, domain.OrgRoleAdmin)
	platform.AddMember(matrixOrgB, policyOtherOrgID, domain.OrgRoleDeveloper)

	functions := edgefn.NewFunctionStore(t.TempDir())
	seedPolicyFunction(t, functions)

	cfg := config.AppConfig{DeploymentMode: "cloud", AppHostingEnabled: true}
	deps := policyDeps(t, instances, platform, functions)
	router := buildRouter(cfg, platform, instances, deps)
	principals := policyPrincipals(platform)
	reseed := func() {
		policyPrincipals(platform)
		seedProjects()
		seedPolicyFunction(t, functions)
	}
	return &policyHarness{router: router, reseed: reseed}, principals
}

// policyHarness drives the real router. Reseeding before every request is not
// a convenience: /api/auth/logout, the token-revocation routes and the project
// teardown are part of the surface under test, and a matrix that let them
// revoke its own credentials or claim its own project would report 401 and 409
// for every row that happened to run after them.
type policyHarness struct {
	router http.Handler
	reseed func()
}

func (h *policyHarness) request(method, path, token string) (int, string) {
	h.reseed()
	// The runtime metadata callback names its project in the body rather than
	// the path; without it the route answers 400 before authenticating.
	req := httptest.NewRequest(method, path, strings.NewReader(`{"projectId":"`+matrixProjectA+`"}`))
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	h.router.ServeHTTP(w, req)
	return w.Code, w.Body.String()
}

// policyPrincipals registers each synthetic caller's user row and credential
// and returns them paired with the bearer token to send.
func policyPrincipals(platform *fakePlatform) []principal {
	out := []principal{{Principal: routepolicy.Principal{Name: "anonymous", Anonymous: true}}}
	add := func(p routepolicy.Principal, userID, role string, tok domain.AccessToken) {
		platform.Users[userID] = &domain.User{ID: userID, Role: role, Active: true}
		raw := testutil.FixtureToken(p.Name)
		tok.TokenHash = auth.HashToken(raw)
		tok.UserID = userID
		platform.ByHash[tok.TokenHash] = &tok
		out = append(out, principal{Principal: p, token: raw})
	}
	session := domain.AccessToken{Scopes: auth.ScopeSession}
	add(routepolicy.Principal{Name: "orgViewer", OrgRole: domain.OrgRoleViewer}, policyViewerID, "user", session)
	add(routepolicy.Principal{Name: "orgDeveloper", OrgRole: domain.OrgRoleDeveloper}, matrixDevID, "user", session)
	add(routepolicy.Principal{Name: "orgAdmin", OrgRole: domain.OrgRoleAdmin}, policyAdminID, "user", session)
	add(routepolicy.Principal{Name: "orgOwner", OrgRole: domain.OrgRoleOwner}, policyOwnerID, "user", session)
	add(routepolicy.Principal{Name: "otherOrgMember"}, policyOtherOrgID, "user", session)
	add(routepolicy.Principal{Name: "platformAdmin", PlatformAdmin: true}, matrixAdminID, "platform_admin", session)
	add(routepolicy.Principal{Name: "platformAdminReadOnlyPAT", PlatformAdmin: true, ReadOnly: true, Restricted: true},
		matrixAdminID+"-ro", "platform_admin", domain.AccessToken{Scopes: auth.ScopeRead})
	add(routepolicy.Principal{Name: "patBoundToOtherProject", OrgRole: domain.OrgRoleAdmin, Restricted: true, ForeignProject: true},
		policyBoundID, "user", domain.AccessToken{ProjectID: matrixProjectB})
	// A platform admin holding a project-bound PAT: every platform permission,
	// yet a credential narrowed to one tenant. The routes that hand out or
	// destroy platform-wide authority must refuse it.
	add(routepolicy.Principal{Name: "platformAdminBoundPAT", PlatformAdmin: true, Restricted: true, ForeignProject: true},
		matrixAdminID+"-bound", "platform_admin", domain.AccessToken{ProjectID: matrixProjectB})
	addCapability(platform, &out, "svcAuthToken", policySvcAuthID, chartPermissions[svcAuth])
	addCapability(platform, &out, "svcGraphqlToken", policySvcGqlID, chartPermissions[svcGraphql])
	return out
}

func addCapability(platform *fakePlatform, out *[]principal, name, userID string, perms []string) {
	platform.Users[userID] = &domain.User{ID: userID, Role: "platform_admin", Active: true, Kind: domain.UserKindService}
	raw := testutil.FixtureToken(name)
	platform.ByHash[auth.HashToken(raw)] = &domain.AccessToken{
		Name: name, UserID: userID, TokenHash: auth.HashToken(raw), Permissions: perms,
	}
	*out = append(*out, principal{
		Principal: routepolicy.Principal{Name: name, PlatformAdmin: true, Capabilities: perms},
		token:     raw,
	})
}

// policyDeps wires every handler whose mount is conditional, so the walk sees
// the storage, vault, resumable-upload, edge-function and mail-relay routes a
// fully configured deployment mounts. Handlers whose internals the matrix does
// not exercise stay on the shared matrixDeps wiring.
func policyDeps(t *testing.T, instances *fakestore.Instances, platform *fakePlatform, functions *edgefn.FunctionStore) *handlerDeps {
	t.Helper()
	deps := matrixDeps(t, instances)
	users, tokens := fakestore.NewUsers(), &policyTokenStore{Tokens: platform.Tokens}
	for id, u := range platform.Users {
		users.Add(&domain.User{ID: id, Role: u.Role, Active: true})
	}
	deps.orgHandler = newOrgHandler2(platform.Orgs, users, instances)
	deps.authHandler = handler.NewAuthHandler(users, tokens)
	deps.svcAcctHandler = handler.NewServiceAccountHandler(users, tokens, nil)
	deps.realtimeHandler = handler.NewRealtimeHandler(instances, platform.Orgs, nil)
	deps.fnHandler = policyFunctionHandler(t, instances, functions)
	deps.storageHandler = policyStorageHandler(t, instances)
	deps.documentsHandler = handler.NewDocumentBrowserHandler(docbrowser.NewService(
		docbrowser.NewGatewayConnector(docbrowser.GatewayConnectorConfig{Projects: instances}), docbrowser.Options{}))
	deps.vaultHandler = policyVaultHandler(t)
	deps.internalEmail = handler.NewInternalEmailHandler(&countingSender{})
	deps.emailTokensHandler = handler.NewEmailTokensHandler(offlineDB(t), nil, nil, "", "")
	deps.capDeps = &capacityDeps{k8sClient: k8s.NewMockClient(), store: instances}
	// The matrix fires thousands of requests as the same user against the same
	// project; the production limits would answer 429 instead of the
	// authorization outcome under test.
	deps.rlUnauth = custommw.RateLimit(custommw.PerIP, 1_000_000, time.Minute)
	deps.rlAuthed = custommw.RateLimit(custommw.PerUser, 1_000_000, time.Minute)
	deps.rlDataPlane = custommw.RateLimit(custommw.PerProjectAndUser, 1_000_000, time.Second)
	// The mail budget is production's, because a test that raised it would
	// stop asserting the one thing that route's limiter is for.
	deps.rlMailSend = custommw.RateLimit(custommw.PerUser, 5, time.Hour)
	return deps
}

// newOrgHandler2 mirrors production's newOrgHandler for the fakes, which
// implement the two stores separately rather than as one platform store.
func newOrgHandler2(orgs *fakestore.Orgs, users *fakestore.Users, instances *fakestore.Instances) *handler.OrgHandler {
	h := handler.NewOrgHandler(orgs, users)
	h.SetInstanceStore(instances)
	return h
}

// offlineDB is a *sql.DB that never connects. The email-token handler needs a
// non-nil handle; a failed query is a 500, which is not a gate refusal.
func offlineDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("postgres", "postgres://offline:offline@127.0.0.1:1/offline")
	if err != nil {
		t.Fatalf("open offline db: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// seedPolicyFunction deploys the function the /functions/v1 rows are driven
// against. concreteRequestPath renders {fnId} and the /http/* wildcard as "x",
// so an httpAction called "x" is what makes those routes reach their JWT gate
// instead of answering "function not found" — an empty store would never run
// the gate the row asserts. It is re-seeded before every request because
// DELETE /api/projects/{projectId}/functions/{fnId} is itself part of the
// surface under test and removes it.
func seedPolicyFunction(t *testing.T, store *edgefn.FunctionStore) {
	t.Helper()
	if err := store.Save(&edgefn.Function{
		ProjectID: matrixProjectA,
		ID:        "x",
		Name:      "x",
		// Kind is stamped from the bundle, not from this field, so the source
		// has to declare it for the /http/* route to match.
		Files:  []edgefn.File{{Path: "index.ts", Content: `export default { kind: "httpAction", handler: async () => new Response("ok") }`}},
		Active: true,
	}); err != nil {
		t.Fatalf("seed function: %v", err)
	}
}

func policyFunctionHandler(t *testing.T, instances *fakestore.Instances, store *edgefn.FunctionStore) *handler.FunctionHandler {
	t.Helper()
	h := handler.NewFunctionHandler(store, nil, nil, instances, nil, "")
	// Without a signing key the JWT gate cannot tell a bad token from a broken
	// deployment and answers 503; with one, every credential this matrix holds
	// is rejected as what it is — not an end-user JWT.
	h.SetVault(policySigningVault(t))
	// The matrix fires every method of every principal at the two public
	// function routes; the production per-project burst would answer 429.
	h.SetRateLimit(1_000_000, 1_000_000)
	// Mounting the runtime secret is what makes the /internal routes answer
	// 401 to a caller without it rather than "not configured".
	h.SetK8sClient(k8s.NewMockClient(), "runtime:test", "policy-runtime-secret")
	return h
}

// policySigningVault serves one secret: the EC public key the function JWT
// gate verifies end-user tokens against.
func policySigningVault(t *testing.T) vaultclient.VaultClient {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate signing key: %v", err)
	}
	der, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		t.Fatalf("marshal signing key: %v", err)
	}
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der})
	return &signingKeyVault{pem: string(pemBytes)}
}

// signingKeyVault answers the one path the JWT gate reads and refuses the rest.
type signingKeyVault struct {
	vaultclient.VaultClient
	pem string
}

func (v *signingKeyVault) Get(path string) (map[string]string, error) {
	if path != "pki/signing/public" {
		return nil, errors.New("secret not found")
	}
	return map[string]string{"key": v.pem}, nil
}

func (v *signingKeyVault) Sealed() bool { return false }

func policyStorageHandler(t *testing.T, instances *fakestore.Instances) *handler.StorageHandler {
	t.Helper()
	h := handler.NewStorageHandler(nil, instances)
	composer := tusd.NewStoreComposer()
	filestore.New(t.TempDir()).UseIn(composer)
	memorylocker.New().UseIn(composer)
	if err := h.EnableResumableUploads(composer); err != nil {
		t.Fatalf("enable resumable uploads: %v", err)
	}
	return h
}

func policyVaultHandler(t *testing.T) *handler.VaultHandler {
	t.Helper()
	localVault, err := newLocalVault(vault.NewMemoryStore(), true, filepath.Join(t.TempDir(), "unseal.key"), "")
	if err != nil {
		t.Fatalf("newLocalVault: %v", err)
	}
	t.Cleanup(func() { localVault.Close() })
	return handler.NewVaultHandler(localVault)
}

// policyTokenStore completes fakestore.Tokens into a storage.TokenStore so the
// token routes resolve ownership for real instead of panicking.
type policyTokenStore struct{ *fakestore.Tokens }

func (s *policyTokenStore) CreateToken(_ context.Context, t *domain.AccessToken) error {
	s.ByHash[t.TokenHash] = t
	return nil
}

func (s *policyTokenStore) ListTokensByUser(_ context.Context, userID string) ([]*domain.AccessToken, error) {
	out := []*domain.AccessToken{}
	for _, t := range s.ByHash {
		if t.UserID == userID {
			out = append(out, t)
		}
	}
	return out, nil
}

func (s *policyTokenStore) DeleteToken(_ context.Context, hash string) error {
	delete(s.ByHash, hash)
	return nil
}

func (s *policyTokenStore) UpdateTokenExpiry(context.Context, string, *time.Time) error { return nil }
func (s *policyTokenStore) TouchTokenLastUsed(context.Context, string, time.Time) error { return nil }

// mountedRoutes returns every method + pattern the router registers.
func mountedRoutes(t *testing.T, router http.Handler) []routepolicy.Key {
	t.Helper()
	mux, ok := router.(*chi.Mux)
	if !ok {
		t.Fatal("buildRouter must return a *chi.Mux")
	}
	var keys []routepolicy.Key
	walk := func(method, pattern string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		keys = append(keys, routepolicy.Key{Method: method, Pattern: pattern})
		return nil
	}
	if err := chi.Walk(mux, walk); err != nil {
		t.Fatalf("walk router: %v", err)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i].String() < keys[j].String() })
	return keys
}

// TestEveryMountedRouteHasAPolicyRow is the guard: a route mounted without a
// row fails here, and a row left behind by a deleted route fails here too.
func TestEveryMountedRouteHasAPolicyRow(t *testing.T) {
	harness, _ := policyRouter(t)
	index, err := routepolicy.Index()
	if err != nil {
		t.Fatalf("route policy table: %v", err)
	}
	matched := map[routepolicy.Key]bool{}
	for _, key := range mountedRoutes(t, harness.router) {
		if _, ok := index[key]; !ok {
			t.Errorf("mounted route %s has no row in internal/routepolicy — declare who may call it", key)
			continue
		}
		matched[key] = true
	}
	for key := range index {
		if !matched[key] {
			t.Errorf("route policy declares %s, which the router does not mount — the row is stale", key)
		}
	}
}

// concreteRequestPath turns a chi pattern into a request path aimed at the
// org-A tenant, so the project and org params name resources the synthetic
// members really belong to.
func concreteRequestPath(pattern string) string {
	path := strings.ReplaceAll(pattern, "{"+routepolicy.ParamProject+"}", matrixProjectA)
	path = strings.ReplaceAll(path, "{"+routepolicy.ParamOrg+"}", matrixOrgA)
	path = strings.ReplaceAll(path, "*", "x")
	for strings.Contains(path, "{") {
		start, end := strings.Index(path, "{"), strings.Index(path, "}")
		path = path[:start] + "x" + path[end+1:]
	}
	return path
}

// TestRoutePolicyMatrix drives the real router with every principal on every
// row and asserts the allow/deny outcome the row implies.
func TestRoutePolicyMatrix(t *testing.T) {
	harness, principals := policyRouter(t)
	index, err := routepolicy.Index()
	if err != nil {
		t.Fatalf("route policy table: %v", err)
	}
	for key, row := range index {
		path := concreteRequestPath(key.Pattern)
		for _, p := range principals {
			want := routepolicy.Expect(row, key.Method, p.Principal)
			if want == routepolicy.Unasserted {
				continue
			}
			t.Run(key.String()+"/"+p.Name, func(t *testing.T) {
				code, body := harness.request(key.Method, path, p.token)
				assertOutcome(t, want, code, body)
			})
		}
	}
}

func assertOutcome(t *testing.T, want routepolicy.Expectation, code int, body string) {
	t.Helper()
	if want == routepolicy.Allow {
		if isGateRefusal(body) {
			t.Fatalf("gate refused with %d (%s); the row says this caller may reach the handler", code, strings.TrimSpace(body))
		}
		return
	}
	if code != expectedStatus(want) {
		t.Fatalf("got %d (%s), want %d", code, strings.TrimSpace(body), expectedStatus(want))
	}
	if !isGateRefusal(body) {
		t.Fatalf("got the right status %d but body %q is not a gate refusal — the handler answered, the gate did not", code, strings.TrimSpace(body))
	}
}

func expectedStatus(want routepolicy.Expectation) int {
	switch want {
	case routepolicy.Deny401:
		return http.StatusUnauthorized
	case routepolicy.Deny403:
		return http.StatusForbidden
	default:
		return http.StatusNotFound
	}
}

// TestCrossTenantProjectParam covers the second tenant dimension: for every
// route naming a {projectId}, a caller with full authority in org A must not
// reach org B's project — whether or not the route runs RequireProjectAccess.
func TestCrossTenantProjectParam(t *testing.T) {
	harness, principals := policyRouter(t)
	index, err := routepolicy.Index()
	if err != nil {
		t.Fatalf("route policy table: %v", err)
	}
	owner := principalNamed(t, principals, "orgOwner")
	checked := 0
	for key, row := range index {
		if !strings.Contains(key.Pattern, "{"+routepolicy.ParamProject+"}") || row.Auth != routepolicy.AuthSession {
			continue
		}
		if key.Method == http.MethodOptions {
			// The CORS middleware answers every preflight at the root, above
			// routing — no tenant is resolved and nothing is disclosed.
			continue
		}
		if row.Permission != "" {
			// A platform-permission route refuses a tenant before it reads the
			// project id at all, so 403 is its answer and it confirms nothing.
			t.Logf("%s is platform-permission gated; a tenant is refused 403 before the project id is read", key)
			continue
		}
		checked++
		path := strings.ReplaceAll(concreteRequestPath(key.Pattern), matrixProjectA, matrixProjectB)
		t.Run(key.String(), func(t *testing.T) {
			code, body := harness.request(key.Method, path, owner.token)
			if code != http.StatusNotFound || !isGateRefusal(body) {
				t.Fatalf("org-A owner reached org-B's project: got %d (%s), want 404", code, strings.TrimSpace(body))
			}
		})
	}
	if checked == 0 {
		t.Fatal("no project-scoped rows were checked; the table or the walk lost them")
	}
}

func principalNamed(t *testing.T, principals []principal, name string) principal {
	t.Helper()
	for _, p := range principals {
		if p.Name == name {
			return p
		}
	}
	t.Fatalf("principal %q is not in the matrix", name)
	return principal{}
}

// EXC-418: /api/setup/status takes no credential, so the only thing standing
// between it and an unbounded request loop is the per-IP limiter. It is
// mounted here because the route is anonymous, not despite it.
func TestSetupStatusIsRateLimited(t *testing.T) {
	instances := fakestore.NewInstances()
	platform := &fakePlatform{Orgs: fakestore.NewOrgs(), Tokens: fakestore.NewTokens()}
	deps := policyDeps(t, instances, platform, edgefn.NewFunctionStore(t.TempDir()))
	deps.rlUnauth = custommw.RateLimit(custommw.PerIP, 2, time.Minute)
	router := buildRouter(config.AppConfig{DeploymentMode: "cloud"}, platform, instances, deps)

	codes := make([]int, 0, 4)
	for i := 0; i < 4; i++ {
		w := httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/setup/status", nil))
		codes = append(codes, w.Code)
	}
	if codes[len(codes)-1] != http.StatusTooManyRequests {
		t.Fatalf("four anonymous polls answered %v; the last must be refused", codes)
	}
}

// EXC-418: the verification send used the general authenticated limiter, 600 a
// minute — enough for one caller to mail themselves into a blocked sending
// domain. It carries its own, far smaller budget.
func TestVerificationSendHasItsOwnSmallBudget(t *testing.T) {
	instances := fakestore.NewInstances()
	platform := &fakePlatform{Orgs: fakestore.NewOrgs(), Tokens: fakestore.NewTokens()}
	deps := policyDeps(t, instances, platform, edgefn.NewFunctionStore(t.TempDir()))
	principals := policyPrincipals(platform)
	router := buildRouter(config.AppConfig{DeploymentMode: "cloud"}, platform, instances, deps)
	caller := principalNamed(t, principals, "orgDeveloper")

	send := func() int {
		req := httptest.NewRequest(http.MethodPost, "/api/email/verify/send", nil)
		req.Header.Set("Authorization", "Bearer "+caller.token)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		return w.Code
	}
	// The general authenticated limiter allows 600 a minute, so a budget that
	// refuses well before that is the one under test.
	refusedAt := 0
	for i := 1; i <= 60 && refusedAt == 0; i++ {
		if send() == http.StatusTooManyRequests {
			refusedAt = i
		}
	}
	if refusedAt == 0 {
		t.Fatal("60 sends by one caller were all accepted")
	}
}
