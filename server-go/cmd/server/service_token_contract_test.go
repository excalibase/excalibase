package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/auth"
	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/handler"
	pgstore "github.com/excalibase/provisioning-poc/internal/storage/postgres"
	"github.com/excalibase/provisioning-poc/internal/testutil"
	"github.com/excalibase/provisioning-poc/internal/testutil/fakestore"
	"github.com/excalibase/provisioning-poc/pkg/vault"
)

// EXC-365: the capability table in internal/middleware/capability.go is a
// hand-maintained allowlist. Nothing in the build compares it against what the
// platform's own services actually call, so a route added later — table-grants
// (EXC-370), the vault credential read, the mail relay — silently answers 403
// to the service that needs it and the failure only surfaces in an e2e run.
//
// This file is that comparison: the inventory below is every control-plane
// endpoint each service calls, driven against the REAL router built by
// buildRouter with the principal's real chart permission list.
//
// Sources of the inventory:
//   engine   excalibase-graphql   ProvisioningPolicyProvider (rls-policies,
//            column-policies, table-grants), ProvisioningProjectCorsProvider
//            (project info), VaultCredentialService (excalibase_app creds)
//   auth     excalibase-auth      cmd/server/main.go (signing key),
//            internal/pool/manager.go (auth_admin creds, project info)
//   watcher  excalibase-watcher   makes no control-plane HTTP call at all

// chartPermissions is the permission list each service principal is minted
// with. It must stay identical to serviceTokens.<service>.permissions in
// excalibase-service charts/platform-aio/values.yaml — the bootstrap Job
// passes those verbatim to the token-minting endpoint. This is the only copy
// in the package; the other cmd/server authz tests read it from here.
var chartPermissions = map[string][]string{
	svcAuth: {
		"vault:read:pki/signing/*",
		"vault:read:projects/*/credentials/auth_admin",
		"projects:info:read",
		"email:send",
	},
	svcGraphql: {
		"vault:read:projects/*/credentials/excalibase_app",
		"projects:info:read",
		"policies:read",
	},
}

const (
	svcAuth    = "svcAuth"
	svcGraphql = "svcGraphql"

	contractProject = "proj-a"
	svcAuthUserID   = "svc-auth-1"
	svcGraphqlUser  = "svc-graphql-1"
)

// serviceCall is one control-plane endpoint a service principal depends on.
type serviceCall struct {
	caller, method, path, capability, source string
}

// platformCalls is the complete inventory: every request the platform's own
// services make, with the capability RequiredCapability demands for it.
var platformCalls = []serviceCall{
	{svcGraphql, http.MethodGet, "/api/provision/proj-a/rls-policies/", "policies:read", "engine ProvisioningPolicyProvider"},
	{svcGraphql, http.MethodGet, "/api/provision/proj-a/column-policies/", "policies:read", "engine ProvisioningPolicyProvider"},
	{svcGraphql, http.MethodGet, "/api/provision/proj-a/table-grants/", "policies:read", "engine ProvisioningPolicyProvider"},
	{svcGraphql, http.MethodGet, "/api/projects/proj-a/info", "projects:info:read", "engine ProvisioningProjectCorsProvider"},
	{svcGraphql, http.MethodGet, "/api/vault/secrets/projects/proj-a/credentials/excalibase_app", "vault:read:projects/proj-a/credentials/excalibase_app", "engine VaultCredentialService"},

	{svcAuth, http.MethodGet, "/api/vault/secrets/pki/signing/private", "vault:read:pki/signing/private", "auth cmd/server fetchSigningKey"},
	{svcAuth, http.MethodGet, "/api/vault/secrets/projects/proj-a/credentials/auth_admin", "vault:read:projects/proj-a/credentials/auth_admin", "auth pool.Manager fetchCredentials"},
	{svcAuth, http.MethodGet, "/api/projects/proj-a/info", "projects:info:read", "auth pool.Manager GetProjectInfo"},
	{svcAuth, http.MethodPost, "/internal/email/send", "email:send", "auth mail relay"},

	// Every capability token may read its own identity.
	{svcAuth, http.MethodGet, "/api/auth/me", "self:read", "service credential self-check"},
	{svcGraphql, http.MethodGet, "/api/auth/me", "self:read", "service credential self-check"},
}

// forbiddenCalls are requests each principal must NOT reach: another service's
// secrets, any write to the policy/exposure surface, and the platform's own
// administrative doors.
var forbiddenCalls = []serviceCall{
	{svcGraphql, http.MethodGet, "/api/vault/secrets/projects/proj-a/credentials/auth_admin", "", "the auth service's role"},
	{svcGraphql, http.MethodGet, "/api/vault/secrets/projects/proj-a/credentials/admin", "", "the project owner's role"},
	{svcGraphql, http.MethodGet, "/api/vault/secrets/pki/signing/private", "", "the platform signing key"},
	{svcGraphql, http.MethodPost, "/api/provision/proj-a/table-grants/", "", "granting itself exposure"},
	{svcGraphql, http.MethodPatch, "/api/provision/proj-a/table-grants/g-1", "", "editing a grant"},
	{svcGraphql, http.MethodDelete, "/api/provision/proj-a/table-grants/g-1", "", "deleting a grant"},
	{svcGraphql, http.MethodPost, "/api/provision/proj-a/rls-policies/", "", "writing a row policy"},
	{svcGraphql, http.MethodPost, "/internal/email/send", "", "sending platform mail"},
	{svcGraphql, http.MethodGet, "/api/provision/proj-a/credentials", "", "the project's own credentials"},

	{svcAuth, http.MethodGet, "/api/vault/secrets/projects/proj-a/credentials/excalibase_app", "", "the engine's role"},
	{svcAuth, http.MethodGet, "/api/provision/proj-a/rls-policies/", "", "the policy surface"},
	{svcAuth, http.MethodGet, "/api/provision/proj-a/table-grants/", "", "the exposure surface"},
	{svcAuth, http.MethodPost, "/api/auth/tokens", "", "minting further tokens"},
	{svcAuth, http.MethodGet, "/api/admin/projects", "", "the platform admin surface"},
}

// memoryGrantStore is an in-memory storage.TableGrantStore so the exposure
// routes answer for real instead of panicking on a nil dependency.
type memoryGrantStore struct {
	mu     sync.Mutex
	grants map[string]domain.TableGrant
}

func newMemoryGrantStore() *memoryGrantStore {
	return &memoryGrantStore{grants: map[string]domain.TableGrant{}}
}

func (m *memoryGrantStore) ListGrants(_ context.Context, projectID string) ([]domain.TableGrant, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []domain.TableGrant{}
	for _, g := range m.grants {
		if g.ProjectID == projectID {
			out = append(out, g)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Resource < out[j].Resource })
	return out, nil
}

func (m *memoryGrantStore) GetGrant(_ context.Context, projectID, id string) (*domain.TableGrant, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	g, ok := m.grants[id]
	if !ok || g.ProjectID != projectID {
		return nil, pgstore.ErrGrantNotFound
	}
	copied := g
	return &copied, nil
}

func (m *memoryGrantStore) UpsertGrant(_ context.Context, g *domain.TableGrant) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.grants[g.ID] = *g
	return nil
}

func (m *memoryGrantStore) DeleteGrant(_ context.Context, projectID, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if g, ok := m.grants[id]; !ok || g.ProjectID != projectID {
		return pgstore.ErrGrantNotFound
	}
	delete(m.grants, id)
	return nil
}

// emptyPolicyStore is a storage.RlsPolicyStore holding nothing. The contract
// asserts authorization, not policy content — the policy routes only need to
// answer instead of panicking on a nil dependency.
type emptyPolicyStore struct{}

func (emptyPolicyStore) ListRls(context.Context, string, string) ([]domain.Policy, error) {
	return []domain.Policy{}, nil
}

func (emptyPolicyStore) GetRls(context.Context, string, string) (*domain.Policy, error) {
	return nil, pgstore.ErrPolicyNotFound
}
func (emptyPolicyStore) UpsertRls(context.Context, *domain.Policy) error { return nil }
func (emptyPolicyStore) DeleteRls(context.Context, string, string) error { return nil }
func (emptyPolicyStore) ListColumn(context.Context, string, string) ([]domain.ColumnPolicy, error) {
	return []domain.ColumnPolicy{}, nil
}

func (emptyPolicyStore) GetColumn(context.Context, string, string) (*domain.ColumnPolicy, error) {
	return nil, pgstore.ErrPolicyNotFound
}
func (emptyPolicyStore) UpsertColumn(context.Context, *domain.ColumnPolicy) error { return nil }
func (emptyPolicyStore) DeleteColumn(context.Context, string, string) error       { return nil }

// contractRouter builds the production router with every dependency the
// inventory touches wired — vault seeded, exposure store, mail relay — and
// returns the raw bearer token of each service principal.
func contractRouter(t *testing.T) (http.Handler, callers, *memoryGrantStore) {
	return contractRouterWithProjectStatus(t, "ACTIVE")
}

// contractRouterWithProjectStatus is contractRouter with the contract
// project in a chosen lifecycle state.
func contractRouterWithProjectStatus(t *testing.T, status string) (http.Handler, callers, *memoryGrantStore) {
	t.Helper()
	localVault, err := newLocalVault(vault.NewMemoryStore(), true, filepath.Join(t.TempDir(), "unseal.key"), "")
	if err != nil {
		t.Fatalf("newLocalVault: %v", err)
	}
	t.Cleanup(func() { localVault.Close() })
	for _, secret := range []string{
		"pki/signing/private",
		"projects/" + contractProject + "/credentials/auth_admin",
		"projects/" + contractProject + "/credentials/excalibase_app",
		"projects/" + contractProject + "/credentials/admin",
	} {
		if err := localVault.Put(secret, map[string]string{"username": "u", "password": "p"}); err != nil {
			t.Fatalf("seed %s: %v", secret, err)
		}
	}

	instances := fakestore.NewInstances()
	instances.Create(&domain.DatabaseInstance{ProjectID: contractProject, OrgID: matrixOrgA, Status: status})

	platform := &fakePlatform{Orgs: fakestore.NewOrgs(), Tokens: fakestore.NewTokens()}
	platform.Users[svcAuthUserID] = &domain.User{ID: svcAuthUserID, Role: "platform_admin", Active: true, Kind: domain.UserKindService}
	platform.Users[svcGraphqlUser] = &domain.User{ID: svcGraphqlUser, Role: "platform_admin", Active: true, Kind: domain.UserKindService}

	who := callers{}
	for name, userID := range map[string]string{svcAuth: svcAuthUserID, svcGraphql: svcGraphqlUser} {
		raw := testutil.FixtureToken(name)
		platform.ByHash[auth.HashToken(raw)] = &domain.AccessToken{
			Name: name, UserID: userID, TokenHash: auth.HashToken(raw), Permissions: chartPermissions[name],
		}
		who[name] = raw
	}

	grants := newMemoryGrantStore()
	deps := matrixDeps(t, instances)
	deps.vaultHandler = handler.NewVaultHandler(localVault)
	deps.vaultHandler.SetInstanceStore(instances)
	deps.tableGrantHandler = handler.NewTableGrantHandler(grants, true)
	deps.rlsPolicyHandler = handler.NewRlsPolicyHandler(emptyPolicyStore{})
	deps.internalEmail = handler.NewInternalEmailHandler(&countingSender{})
	cfg := config.AppConfig{DeploymentMode: "selfhosted"}
	return buildRouter(cfg, platform, instances, deps), who, grants
}

func contractRequest(router http.Handler, call serviceCall, token string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(call.method, call.path, strings.NewReader(`{"to":"u@example.com","projectId":"proj-a","template":"verify_email"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	return w
}

// TestPlatformServiceCallsAreAuthorized is the contract: every request the
// platform's own services make must get past authentication and the
// capability gate. A route the capability table forgot answers 403 here
// instead of in an e2e run.
func TestPlatformServiceCallsAreAuthorized(t *testing.T) {
	router, who, _ := contractRouter(t)
	for _, call := range platformCalls {
		t.Run(call.caller+" "+call.method+" "+call.path, func(t *testing.T) {
			w := contractRequest(router, call, who[call.caller])
			if w.Code == http.StatusUnauthorized || w.Code == http.StatusForbidden {
				t.Fatalf("%s (%s) refused with %d: %s — needs capability %q",
					call.path, call.source, w.Code, w.Body.String(), call.capability)
			}
		})
	}
}

func TestPlatformServicesAreRefusedEverythingElse(t *testing.T) {
	router, who, _ := contractRouter(t)
	for _, call := range forbiddenCalls {
		t.Run(call.caller+" "+call.method+" "+call.path, func(t *testing.T) {
			w := contractRequest(router, call, who[call.caller])
			if w.Code != http.StatusForbidden {
				t.Fatalf("%s (%s) got %d, want 403: %s", call.path, call.source, w.Code, w.Body.String())
			}
		})
	}
}

// A project the platform must not serve must not have its credentials handed
// to the engine or the auth service either — that is the door they actually
// use. 404, so a service treats the project as absent and stops serving it.
func TestServicesGetNoCredentialsForAProjectThatIsNotServable(t *testing.T) {
	for name, status := range map[string]string{
		"restoring": string(domain.StatusRestoring),
		"deleting":  string(domain.StatusDeleting),
	} {
		t.Run(name, func(t *testing.T) {
			router, who, _ := contractRouterWithProjectStatus(t, status)
			for _, call := range []serviceCall{
				{svcGraphql, http.MethodGet, "/api/vault/secrets/projects/" + contractProject + "/credentials/excalibase_app", "", ""},
				{svcAuth, http.MethodGet, "/api/vault/secrets/projects/" + contractProject + "/credentials/auth_admin", "", ""},
			} {
				w := contractRequest(router, call, who[call.caller])
				if w.Code != http.StatusNotFound {
					t.Errorf("%s %s: got %d, want 404; body=%s", call.caller, call.path, w.Code, w.Body.String())
				}
			}
		})
	}
}
