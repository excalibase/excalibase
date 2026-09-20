package handler

import (
	"bytes"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/auth"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/testutil/fakestore"
	"github.com/excalibase/provisioning-poc/pkg/vault"
	"github.com/go-chi/chi/v5"
)

// EXC-418: a human caller holding view_credentials could read ANY project's
// database credentials, and nothing recorded that they had.
//
// These cases drive the REAL mount — RequireAuth, then
// RequirePermission(view_credentials), then the handler — because only two
// roles hold view_credentials at all. A gate expressed in terms of a
// permission those two roles already carry would refuse nobody who can reach
// the route, so the principals here are the ones that actually get there:
// platform_admin, platform_operator, and a service principal.

const (
	vaultOrgA       = "org-a"
	vaultProjectA   = "proj-vault-a"
	vaultProjectB   = "proj-vault-b"
	vaultMemberA    = "member-vault-a"
	vaultSecretPath = "projects/" + vaultProjectA + "/credentials/excalibase_app"
	vaultSecretURL  = "/api/vault/secrets/" + vaultSecretPath
)

func vaultScopeHandler(t *testing.T) *VaultHandler {
	t.Helper()
	v, err := vault.NewWithStore(vault.NewMemoryStore())
	if err != nil {
		t.Fatalf("new vault: %v", err)
	}
	res, err := v.Init(1, 1)
	if err != nil {
		t.Fatalf("init vault: %v", err)
	}
	if _, err := v.Unseal(res.Shares[0]); err != nil {
		t.Fatalf("unseal vault: %v", err)
	}
	for _, path := range []string{vaultSecretPath, "projects/" + vaultProjectB + "/credentials/excalibase_app"} {
		if err := v.Put(path, map[string]string{"password": "s3cr3t"}); err != nil {
			t.Fatalf("seed %s: %v", path, err)
		}
	}

	instances := fakestore.NewInstances()
	instances.Items[vaultProjectA] = &domain.DatabaseInstance{ProjectID: vaultProjectA, OrgID: vaultOrgA, Status: "ACTIVE"}
	instances.Items[vaultProjectB] = &domain.DatabaseInstance{ProjectID: vaultProjectB, OrgID: "org-b", Status: "ACTIVE"}
	orgs := fakestore.NewOrgs()
	orgs.AddMember(vaultOrgA, vaultMemberA, domain.OrgRoleAdmin)

	h := NewVaultHandler(v)
	h.SetInstanceStore(instances)
	h.SetOrgStore(orgs)
	return h
}

// vaultAs mounts the production vault routes behind a middleware that injects
// one principal, so every request meets the real gate chain.
func vaultAs(h *VaultHandler, user *domain.User, token *domain.AccessToken) http.Handler {
	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			ctx := req.Context()
			if user != nil {
				ctx = auth.SetUser(ctx, user)
			}
			if token != nil {
				ctx = auth.SetToken(ctx, token)
			}
			next.ServeHTTP(w, req.WithContext(ctx))
		})
	})
	r.Route("/api/vault", h.Routes)
	return r
}

func vaultGet(t *testing.T, h *VaultHandler, url string, user *domain.User, token *domain.AccessToken) (int, string) {
	t.Helper()
	w := httptest.NewRecorder()
	vaultAs(h, user, token).ServeHTTP(w, httptest.NewRequest(http.MethodGet, url, nil))
	return w.Code, strings.TrimSpace(w.Body.String())
}

func operatorUser() *domain.User {
	return &domain.User{ID: "op", Role: "platform_operator", Active: true}
}

func adminUser() *domain.User {
	return &domain.User{ID: "admin", Role: "platform_admin", Active: true}
}

func sessionToken() *domain.AccessToken {
	return &domain.AccessToken{Scopes: auth.ScopeSession}
}

// The finding: a platform_operator with a session cookie and no membership of
// the project could read its database password.
func TestVaultSecretReadRefusesAnOperatorWithNoAccessToTheProject(t *testing.T) {
	h := vaultScopeHandler(t)

	code, body := vaultGet(t, h, vaultSecretURL, operatorUser(), sessionToken())
	if code != http.StatusNotFound {
		t.Fatalf("got %d (%s), want 404", code, body)
	}
	if strings.Contains(body, "s3cr3t") {
		t.Fatal("the refusal carried the secret")
	}
}

func TestVaultSecretReadAllowsAnOperatorWhoIsAMemberOfTheProjectsOrg(t *testing.T) {
	h := vaultScopeHandler(t)
	member := &domain.User{ID: vaultMemberA, Role: "platform_operator", Active: true}

	if code, body := vaultGet(t, h, vaultSecretURL, member, sessionToken()); code != http.StatusOK {
		t.Fatalf("got %d (%s), want 200", code, body)
	}
}

// platform_admin keeps the platform-wide bypass it holds on every other
// project-bound route. It is the one operator exception, and it is audited.
func TestVaultSecretReadAllowsAPlatformAdmin(t *testing.T) {
	h := vaultScopeHandler(t)

	if code, body := vaultGet(t, h, vaultSecretURL, adminUser(), sessionToken()); code != http.StatusOK {
		t.Fatalf("got %d (%s), want 200", code, body)
	}
}

func TestVaultSecretReadRefusesATokenBoundElsewhere(t *testing.T) {
	h := vaultScopeHandler(t)

	code, _ := vaultGet(t, h, vaultSecretURL, adminUser(), &domain.AccessToken{ProjectID: vaultProjectB})
	if code != http.StatusNotFound {
		t.Fatalf("got %d, want 404: a token bound to project B read project A's secret", code)
	}
}

// A tenant never reaches the handler at all: the permission gate above it
// refuses them first.
func TestVaultSecretReadRefusesATenantAtThePermissionGate(t *testing.T) {
	h := vaultScopeHandler(t)
	tenant := &domain.User{ID: vaultMemberA, Role: "user", Active: true}

	if code, _ := vaultGet(t, h, vaultSecretURL, tenant, sessionToken()); code != http.StatusForbidden {
		t.Fatalf("got %d, want 403", code)
	}
}

// A capability token is the platform's own service principal. The capability
// gate above the route already names the one secret it may read, so this
// handler must leave it exactly as it was.
func TestVaultSecretReadLeavesServicePrincipalsAlone(t *testing.T) {
	h := vaultScopeHandler(t)
	svc := &domain.User{ID: "svc-auth", Role: "platform_admin", Active: true, Kind: domain.UserKindService}
	token := &domain.AccessToken{Name: "svc-auth", Permissions: []string{"vault:read:" + vaultSecretPath}}

	if code, body := vaultGet(t, h, vaultSecretURL, svc, token); code != http.StatusOK {
		t.Fatalf("got %d (%s), want 200", code, body)
	}
}

func TestVaultSecretReadRefusesAnUnauthenticatedCaller(t *testing.T) {
	h := vaultScopeHandler(t)
	if code, _ := vaultGet(t, h, vaultSecretURL, nil, nil); code != http.StatusUnauthorized {
		t.Fatalf("got %d, want 401", code)
	}
}

// Without the stores there is no way to bind the read, and serving it anyway
// is the hole this closes.
func TestVaultSecretReadFailsClosedWithoutTheProjectStores(t *testing.T) {
	h := NewVaultHandler(mustUnsealedVault(t))
	if code, _ := vaultGet(t, h, vaultSecretURL, operatorUser(), sessionToken()); code != http.StatusNotFound {
		t.Fatalf("got %d, want 404", code)
	}
}

func mustUnsealedVault(t *testing.T) *vault.Vault {
	t.Helper()
	v, err := vault.NewWithStore(vault.NewMemoryStore())
	if err != nil {
		t.Fatalf("new vault: %v", err)
	}
	res, err := v.Init(1, 1)
	if err != nil {
		t.Fatalf("init vault: %v", err)
	}
	if _, err := v.Unseal(res.Shares[0]); err != nil {
		t.Fatalf("unseal vault: %v", err)
	}
	return v
}

func captureLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var logged bytes.Buffer
	previous := log.Writer()
	log.SetOutput(&logged)
	t.Cleanup(func() { log.SetOutput(previous) })
	return &logged
}

func assertAudited(t *testing.T, logged *bytes.Buffer, event string, names ...string) {
	t.Helper()
	line := logged.String()
	if !strings.Contains(line, event) {
		t.Fatalf("no %s line in %q", event, strings.TrimSpace(line))
	}
	for _, want := range names {
		if !strings.Contains(line, want) {
			t.Errorf("audit line %q does not name %q", strings.TrimSpace(line), want)
		}
	}
	if strings.Contains(line, "s3cr3t") {
		t.Fatal("the audit line carried the secret value")
	}
}

func TestVaultSecretReadIsAudited(t *testing.T) {
	h := vaultScopeHandler(t)
	logged := captureLog(t)

	if code, body := vaultGet(t, h, vaultSecretURL, adminUser(), sessionToken()); code != http.StatusOK {
		t.Fatalf("got %d (%s)", code, body)
	}
	assertAudited(t, logged, "vault.secret.read", "admin", vaultProjectA, vaultSecretPath)
}

// Service principals fetch tenant credentials for a living: they are the bulk
// of all secret reads, and an audit trail that omits them records nothing.
func TestVaultSecretReadByAServicePrincipalIsAudited(t *testing.T) {
	h := vaultScopeHandler(t)
	logged := captureLog(t)
	svc := &domain.User{ID: "svc-graphql", Role: "platform_admin", Active: true, Kind: domain.UserKindService}
	token := &domain.AccessToken{Name: "svc-graphql", Permissions: []string{"vault:read:" + vaultSecretPath}}

	if code, body := vaultGet(t, h, vaultSecretURL, svc, token); code != http.StatusOK {
		t.Fatalf("got %d (%s)", code, body)
	}
	assertAudited(t, logged, "vault.secret.read", "svc-graphql", vaultProjectA, vaultSecretPath)
}

// --- secrets-list ---

// Listing by prefix enumerates secret paths. Bound to a project, it is the
// same read as any other; unbound, it is an inventory of every tenant.
func TestVaultSecretListRefusesAnOperatorOnAnotherProjectsPrefix(t *testing.T) {
	h := vaultScopeHandler(t)

	code, _ := vaultGet(t, h, "/api/vault/secrets-list?prefix=projects/"+vaultProjectA+"/", operatorUser(), sessionToken())
	if code != http.StatusNotFound {
		t.Fatalf("got %d, want 404", code)
	}
}

func TestVaultSecretListAllowsAMemberOnTheirOwnProjectsPrefix(t *testing.T) {
	h := vaultScopeHandler(t)
	member := &domain.User{ID: vaultMemberA, Role: "platform_operator", Active: true}

	code, body := vaultGet(t, h, "/api/vault/secrets-list?prefix=projects/"+vaultProjectA, member, sessionToken())
	if code != http.StatusOK {
		t.Fatalf("got %d (%s), want 200", code, body)
	}
	if !strings.Contains(body, vaultSecretPath) {
		t.Fatalf("the listing dropped the caller's own paths: %s", body)
	}
}

func TestVaultSecretListRefusesACrossTenantPrefixToAnOperator(t *testing.T) {
	h := vaultScopeHandler(t)

	for _, prefix := range []string{"", "?prefix=projects/", "?prefix="} {
		code, _ := vaultGet(t, h, "/api/vault/secrets-list"+prefix, operatorUser(), sessionToken())
		if code != http.StatusForbidden {
			t.Errorf("prefix %q: got %d, want 403", prefix, code)
		}
	}
}

// The platform admin's cross-tenant listing is what the install checks use;
// it stays, and it is audited.
func TestVaultSecretListAllowsAPlatformAdminAcrossTenants(t *testing.T) {
	h := vaultScopeHandler(t)
	logged := captureLog(t)

	code, body := vaultGet(t, h, "/api/vault/secrets-list", adminUser(), sessionToken())
	if code != http.StatusOK {
		t.Fatalf("got %d (%s), want 200", code, body)
	}
	assertAudited(t, logged, "vault.secret.list", "admin")
}

func TestVaultSecretListRefusesACrossTenantPrefixToABoundCredential(t *testing.T) {
	h := vaultScopeHandler(t)

	code, _ := vaultGet(t, h, "/api/vault/secrets-list", adminUser(), &domain.AccessToken{ProjectID: vaultProjectA})
	if code != http.StatusForbidden {
		t.Fatalf("got %d, want 403", code)
	}
}

// vaultProjectStores wires the project resolution a secret read now runs. The
// handler fails closed without it, so every vault test harness needs it.
func vaultProjectStores(h *VaultHandler, orgID string, members []string, projectIDs ...string) {
	instances := fakestore.NewInstances()
	for _, projectID := range projectIDs {
		instances.Items[projectID] = &domain.DatabaseInstance{ProjectID: projectID, OrgID: orgID, Status: "ACTIVE"}
	}
	orgs := fakestore.NewOrgs()
	for _, member := range members {
		orgs.AddMember(orgID, member, domain.OrgRoleAdmin)
	}
	h.SetInstanceStore(instances)
	h.SetOrgStore(orgs)
}

func TestProjectIDForSecretPrefixTreatsABareProjectAsThatProject(t *testing.T) {
	cases := map[string]string{
		"projects/p1/credentials": "p1",
		"projects/p1/":            "p1",
		"projects/p1":             "p1",
		"projects/":               "",
		"projects":                "",
		"pki/":                    "",
		"":                        "",
	}
	for prefix, want := range cases {
		if got := projectIDForSecretPrefix(prefix); got != want {
			t.Errorf("%q: got %q, want %q", prefix, got, want)
		}
	}
}
