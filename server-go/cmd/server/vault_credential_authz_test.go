package main

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/auth"
	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/handler"
	"github.com/excalibase/provisioning-poc/internal/testutil"
	"github.com/excalibase/provisioning-poc/internal/testutil/fakestore"
	"github.com/excalibase/provisioning-poc/pkg/vault"
)

// EXC-365: each platform service reads exactly one database role's credentials
// out of vault — the auth service auth_admin, the engine excalibase_app — and
// must reach no other role in any project, least of all the project owner's
// admin credential. These tests drive the REAL router built by buildRouter.

const (
	credProjectID    = "proj-1"
	credOtherProject = "tenant-b"

	credCallerAuthSvc    = "svcAuth"
	credCallerGraphqlSvc = "svcGraphql"
	credAuthUserID       = "svc-auth-1"
	credGraphqlUserID    = "svc-graphql-1"

	roleAdmin     = "admin"
	roleAuthAdmin = "auth_admin"
	roleApp       = "excalibase_app"
	roleWatcher   = "cdc_watcher"
)

func credentialRoute(projectID, role string) string {
	return "/api/vault/secrets/projects/" + projectID + "/credentials/" + role
}

// credentialRouter builds the production router over a ready in-memory vault
// holding every role credential of two projects, and returns the raw bearer
// token of each service caller.
func credentialRouter(t *testing.T) (http.Handler, callers) {
	t.Helper()
	localVault, err := newLocalVault(vault.NewMemoryStore(), true, filepath.Join(t.TempDir(), "unseal.key"), "")
	if err != nil {
		t.Fatalf("newLocalVault: %v", err)
	}
	t.Cleanup(func() { localVault.Close() })
	for _, projectID := range []string{credProjectID, credOtherProject} {
		for _, role := range []string{roleAdmin, roleAuthAdmin, roleApp, roleWatcher} {
			secret := map[string]string{"username": role, "password": "pw-" + role}
			if err := localVault.Put("projects/"+projectID+"/credentials/"+role, secret); err != nil {
				t.Fatalf("seed %s/%s: %v", projectID, role, err)
			}
		}
	}

	instances := fakestore.NewInstances()
	platform := &fakePlatform{Orgs: fakestore.NewOrgs(), Tokens: fakestore.NewTokens()}
	platform.Users[credAuthUserID] = &domain.User{ID: credAuthUserID, Role: "platform_admin", Active: true, Kind: domain.UserKindService}
	platform.Users[credGraphqlUserID] = &domain.User{ID: credGraphqlUserID, Role: "platform_admin", Active: true, Kind: domain.UserKindService}

	who := callers{}
	issue := func(name, userID string, permissions []string) {
		raw := testutil.FixtureToken(name)
		platform.ByHash[auth.HashToken(raw)] = &domain.AccessToken{
			Name: name, UserID: userID, TokenHash: auth.HashToken(raw), Permissions: permissions,
		}
		who[name] = raw
	}
	issue(credCallerAuthSvc, credAuthUserID, []string{
		"vault:read:pki/signing/*", "vault:read:projects/*/credentials/auth_admin",
		"projects:info:read", "email:send"})
	issue(credCallerGraphqlSvc, credGraphqlUserID, []string{
		"vault:read:projects/*/credentials/excalibase_app", "projects:info:read", "policies:read"})

	deps := matrixDeps(t, instances)
	deps.vaultHandler = handler.NewVaultHandler(localVault)
	cfg := config.AppConfig{DeploymentMode: "selfhosted"}
	return buildRouter(cfg, platform, instances, deps), who
}

func getCredential(router http.Handler, token, path string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	return w
}

func TestServiceTokensReachOnlyTheirOwnRoleCredential(t *testing.T) {
	cases := []struct {
		name      string
		caller    string
		projectID string
		role      string
		code      int
	}{
		{name: "auth reads auth_admin", caller: credCallerAuthSvc, projectID: credProjectID, role: roleAuthAdmin, code: http.StatusOK},
		{name: "auth reads auth_admin of another project", caller: credCallerAuthSvc, projectID: credOtherProject, role: roleAuthAdmin, code: http.StatusOK},
		{name: "auth refused the engine role", caller: credCallerAuthSvc, projectID: credProjectID, role: roleApp, code: http.StatusForbidden},
		{name: "auth refused the owner role", caller: credCallerAuthSvc, projectID: credProjectID, role: roleAdmin, code: http.StatusForbidden},
		{name: "auth refused the watcher role", caller: credCallerAuthSvc, projectID: credProjectID, role: roleWatcher, code: http.StatusForbidden},

		{name: "engine reads excalibase_app", caller: credCallerGraphqlSvc, projectID: credProjectID, role: roleApp, code: http.StatusOK},
		{name: "engine reads excalibase_app of another project", caller: credCallerGraphqlSvc, projectID: credOtherProject, role: roleApp, code: http.StatusOK},
		{name: "engine refused the auth role", caller: credCallerGraphqlSvc, projectID: credProjectID, role: roleAuthAdmin, code: http.StatusForbidden},
		{name: "engine refused the owner role", caller: credCallerGraphqlSvc, projectID: credProjectID, role: roleAdmin, code: http.StatusForbidden},
		{name: "engine refused the watcher role", caller: credCallerGraphqlSvc, projectID: credProjectID, role: roleWatcher, code: http.StatusForbidden},
	}
	router, who := credentialRouter(t)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := getCredential(router, who[tc.caller], credentialRoute(tc.projectID, tc.role))
			if w.Code != tc.code {
				t.Fatalf("want %d, got %d body=%s", tc.code, w.Code, w.Body.String())
			}
		})
	}
}

// The signing-key grant the auth service already held must keep working, and
// must not have widened into the rest of the vault.
func TestAuthServiceSigningKeyGrantIsUnchanged(t *testing.T) {
	router, who := credentialRouter(t)

	if w := getCredential(router, who[credCallerAuthSvc], "/api/vault/secrets/pki/signing/private"); w.Code != http.StatusOK {
		t.Fatalf("signing key read = %d, want 200", w.Code)
	}
	if w := getCredential(router, who[credCallerAuthSvc], "/api/vault/secrets/pki/signing/sub/key"); w.Code != http.StatusForbidden {
		t.Fatalf("deeper signing path = %d, want 403", w.Code)
	}
	if w := getCredential(router, who[credCallerGraphqlSvc], "/api/vault/secrets/pki/signing/private"); w.Code != http.StatusForbidden {
		t.Fatalf("engine reading the signing key = %d, want 403", w.Code)
	}
}

// A path whose percent-escapes make the gate and chi disagree on which secret
// is being read must be refused outright.
func TestServiceTokenRefusedAnEscapedCredentialPath(t *testing.T) {
	router, who := credentialRouter(t)
	escaped := "/api/vault/secrets/projects/" + credProjectID + "/credentials/auth%5Fadmin"

	if w := getCredential(router, who[credCallerAuthSvc], escaped); w.Code != http.StatusForbidden {
		t.Fatalf("escaped credential path = %d, want 403", w.Code)
	}
}
