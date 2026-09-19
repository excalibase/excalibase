package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/auth"
	"github.com/excalibase/provisioning-poc/internal/domain"
)

// permsAuth is the permission list the auth service principal is minted with.
// Source of truth: serviceTokens.auth.permissions in excalibase-service
// charts/platform-aio/values.yaml; cmd/server's contract test drives the same
// lists against the real router.
var permsAuth = []string{
	"vault:read:pki/signing/*",
	"vault:read:projects/*/credentials/auth_admin",
	"projects:info:read",
	"email:send",
}

// permsGraphql is the permission list the graphql service principal is minted with.
var permsGraphql = []string{
	"vault:read:projects/*/credentials/excalibase_app",
	"projects:info:read",
	"policies:read",
}

// credentialPath is the vault route a service reads one project role's
// connection details from.
func credentialPath(projectID, role string) string {
	return "/api/vault/secrets/projects/" + projectID + "/credentials/" + role
}

func TestRequiredCapability(t *testing.T) {
	cases := []struct {
		name   string
		method string
		path   string
		want   string
		found  bool
	}{
		{name: "vault secret read", method: http.MethodGet, path: "/api/vault/secrets/pki/signing/private",
			want: "vault:read:pki/signing/private", found: true},
		{name: "nested vault secret read", method: http.MethodGet, path: "/api/vault/secrets/projects/p1/db",
			want: "vault:read:projects/p1/db", found: true},
		{name: "project role credential read", method: http.MethodGet, path: credentialPath("p1", "auth_admin"),
			want: "vault:read:projects/p1/credentials/auth_admin", found: true},
		{name: "project info", method: http.MethodGet, path: "/api/projects/proj-1/info", want: "projects:info:read", found: true},
		{name: "project info trailing slash", method: http.MethodGet, path: "/api/projects/proj-1/info/", want: "projects:info:read", found: true},
		{name: "rls policies list", method: http.MethodGet, path: "/api/provision/proj-1/rls-policies", want: "policies:read", found: true},
		{name: "rls policy by id", method: http.MethodGet, path: "/api/provision/proj-1/rls-policies/pol-9", want: "policies:read", found: true},
		{name: "column policies list", method: http.MethodGet, path: "/api/provision/proj-1/column-policies", want: "policies:read", found: true},
		// EXC-370 added the exposure list the engine fetches in the same
		// round as the two policy reads; it belongs to the same grant.
		{name: "table grants list", method: http.MethodGet, path: "/api/provision/proj-1/table-grants", want: "policies:read", found: true},
		{name: "table grants list trailing slash", method: http.MethodGet, path: "/api/provision/proj-1/table-grants/", want: "policies:read", found: true},
		{name: "table grant by id", method: http.MethodGet, path: "/api/provision/proj-1/table-grants/grant-9", want: "policies:read", found: true},
		{name: "head is a read", method: http.MethodHead, path: "/api/provision/proj-1/rls-policies", want: "policies:read", found: true},
		{name: "email relay send", method: http.MethodPost, path: "/internal/email/send", want: "email:send", found: true},

		{name: "email relay read", method: http.MethodGet, path: "/internal/email/send"},
		{name: "email relay sibling route", method: http.MethodPost, path: "/internal/email/send/again"},

		{name: "vault secret write", method: http.MethodPut, path: "/api/vault/secrets/pki/signing/private"},
		{name: "vault secret delete", method: http.MethodDelete, path: "/api/vault/secrets/pki/signing/private"},
		{name: "vault secret list", method: http.MethodGet, path: "/api/vault/secrets-list"},
		{name: "vault unseal", method: http.MethodPost, path: "/api/vault/unseal"},
		{name: "policy write", method: http.MethodPost, path: "/api/provision/proj-1/rls-policies"},
		{name: "grant create", method: http.MethodPost, path: "/api/provision/proj-1/table-grants/"},
		{name: "grant update", method: http.MethodPatch, path: "/api/provision/proj-1/table-grants/grant-9"},
		{name: "grant delete", method: http.MethodDelete, path: "/api/provision/proj-1/table-grants/grant-9"},
		{name: "exposure enforcement toggle", method: http.MethodPut, path: "/api/provision/proj-1/table-grants/enforcement"},
		{name: "project status", method: http.MethodGet, path: "/api/provision/proj-1"},
		{name: "project credentials", method: http.MethodGet, path: "/api/provision/proj-1/credentials"},
		{name: "info of a nested resource", method: http.MethodGet, path: "/api/projects/proj-1/info/extra"},
		{name: "schema browse", method: http.MethodGet, path: "/api/schema/proj-1/tables"},
		{name: "admin listing", method: http.MethodGet, path: "/api/admin/projects"},
		{name: "token minting", method: http.MethodPost, path: "/api/auth/tokens"},
		{name: "empty vault selector", method: http.MethodGet, path: "/api/vault/secrets/"},
		// An empty path is left as-is by normalization and matches no route,
		// so no capability can authorize it.
		{name: "empty path", method: http.MethodGet, path: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := RequiredCapability(tc.method, tc.path)
			if ok != tc.found {
				t.Fatalf("RequiredCapability(%s %s) found = %v, want %v (got %v)", tc.method, tc.path, ok, tc.found, got)
			}
			if ok && got.String() != tc.want {
				t.Fatalf("RequiredCapability(%s %s) = %q, want %q", tc.method, tc.path, got.String(), tc.want)
			}
		})
	}
}

// serveWithToken runs the gate in front of a handler that records that it ran.
func serveWithToken(t *testing.T, token *domain.AccessToken, method, path string) (int, bool) {
	t.Helper()
	reached := false
	handler := CapabilityGate(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reached = true
		w.WriteHeader(http.StatusOK)
	}))
	req := httptest.NewRequest(method, path, nil)
	if token != nil {
		req = req.WithContext(auth.SetToken(req.Context(), token))
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec.Code, reached
}

func TestCapabilityGateAllowsNonCapabilityTokens(t *testing.T) {
	cases := []struct {
		name  string
		token *domain.AccessToken
	}{
		{name: "no token at all", token: nil},
		{name: "human PAT with no permissions", token: &domain.AccessToken{Name: "ci"}},
		{name: "human PAT with scopes only", token: &domain.AccessToken{Name: "ci", Scopes: "admin"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// A route the capability table does not even know about.
			code, reached := serveWithToken(t, tc.token, http.MethodDelete, "/api/provision/proj-1")
			if code != http.StatusOK || !reached {
				t.Fatalf("gate blocked a non-capability token: code=%d reached=%v", code, reached)
			}
		})
	}
}

func TestCapabilityGateEnforcesGraphqlToken(t *testing.T) {
	token := &domain.AccessToken{Name: "svc-graphql", Permissions: permsGraphql}
	allowed := []struct{ method, path string }{
		{http.MethodGet, "/api/projects/proj-1/info"},
		{http.MethodGet, "/api/provision/proj-1/rls-policies"},
		{http.MethodGet, "/api/provision/proj-1/column-policies"},
		{http.MethodGet, "/api/provision/proj-1/table-grants/"},
		{http.MethodGet, "/api/auth/me"},
	}
	for _, c := range allowed {
		code, reached := serveWithToken(t, token, c.method, c.path)
		if code != http.StatusOK || !reached {
			t.Fatalf("%s %s: code=%d reached=%v, want 200", c.method, c.path, code, reached)
		}
	}
	if code, reached := serveWithToken(t, token, http.MethodGet, credentialPath("proj-1", "excalibase_app")); code != http.StatusOK || !reached {
		t.Fatalf("engine credential read = %d reached=%v, want 200", code, reached)
	}
	denied := []struct{ method, path string }{
		{http.MethodGet, "/api/vault/secrets/pki/signing/private"},
		{http.MethodGet, "/api/vault/secrets/projects/proj-1/db"},
		{http.MethodGet, credentialPath("proj-1", "auth_admin")},
		{http.MethodGet, credentialPath("proj-1", "admin")},
		{http.MethodGet, credentialPath("proj-1", "cdc_watcher")},
		{http.MethodPost, "/api/provision/proj-1/rls-policies"},
		{http.MethodPost, "/api/provision/proj-1/table-grants/"},
		{http.MethodPatch, "/api/provision/proj-1/table-grants/grant-9"},
		{http.MethodDelete, "/api/provision/proj-1/table-grants/grant-9"},
		{http.MethodPut, "/api/provision/proj-1/table-grants/enforcement"},
		{http.MethodDelete, "/api/provision/proj-1"},
		{http.MethodGet, "/api/provision/proj-1/credentials"},
		{http.MethodPost, "/api/auth/tokens"},
		{http.MethodGet, "/api/admin/projects"},
		{http.MethodPost, "/internal/email/send"},
	}
	for _, c := range denied {
		code, reached := serveWithToken(t, token, c.method, c.path)
		if code != http.StatusForbidden || reached {
			t.Fatalf("%s %s: code=%d reached=%v, want 403", c.method, c.path, code, reached)
		}
	}
}

func TestCapabilityGateEnforcesAuthToken(t *testing.T) {
	token := &domain.AccessToken{Name: "svc-auth", Permissions: permsAuth}
	if code, _ := serveWithToken(t, token, http.MethodGet, "/api/vault/secrets/pki/signing/private"); code != http.StatusOK {
		t.Fatalf("signing key read = %d, want 200", code)
	}
	if code, _ := serveWithToken(t, token, http.MethodGet, "/api/projects/proj-1/info"); code != http.StatusOK {
		t.Fatalf("project info = %d, want 200", code)
	}
	if code, _ := serveWithToken(t, token, http.MethodPost, "/internal/email/send"); code != http.StatusOK {
		t.Fatalf("email relay = %d, want 200", code)
	}
	if code, reached := serveWithToken(t, token, http.MethodGet, credentialPath("proj-1", "auth_admin")); code != http.StatusOK || !reached {
		t.Fatalf("auth credential read = %d reached=%v, want 200", code, reached)
	}
	if code, reached := serveWithToken(t, token, http.MethodGet, credentialPath("tenant-b", "auth_admin")); code != http.StatusOK || !reached {
		t.Fatalf("auth credential read in another project = %d reached=%v, want 200", code, reached)
	}
	denied := []struct{ method, path string }{
		{http.MethodGet, "/api/vault/secrets/pki/other"},
		{http.MethodGet, "/api/vault/secrets/projects/proj-1/db"},
		{http.MethodGet, credentialPath("proj-1", "excalibase_app")},
		{http.MethodGet, credentialPath("proj-1", "admin")},
		{http.MethodGet, credentialPath("proj-1", "cdc_watcher")},
		{http.MethodGet, credentialPath("proj-1", "auth_admin") + "/password"},
		{http.MethodGet, "/api/vault/secrets/projects/proj-1/credentials"},
		{http.MethodGet, "/api/provision/proj-1/rls-policies"},
		{http.MethodGet, "/api/provision/proj-1/table-grants/"},
		{http.MethodPut, "/api/vault/secrets/pki/signing/private"},
		{http.MethodPut, credentialPath("proj-1", "auth_admin")},
		{http.MethodDelete, credentialPath("proj-1", "auth_admin")},
	}
	for _, c := range denied {
		code, reached := serveWithToken(t, token, c.method, c.path)
		if code != http.StatusForbidden || reached {
			t.Fatalf("%s %s: code=%d reached=%v, want 403", c.method, c.path, code, reached)
		}
	}
}

func TestCapabilityGateNormalizesThePath(t *testing.T) {
	token := &domain.AccessToken{Name: "svc-auth", Permissions: permsAuth}
	// A traversal that would resolve outside the granted subtree must not pass.
	if code, _ := serveWithToken(t, token, http.MethodGet, "/api/vault/secrets/pki/signing/../other"); code != http.StatusForbidden {
		t.Fatalf("traversal out of the granted subtree = %d, want 403", code)
	}
	// A redundant segment that resolves back inside it is still the same read.
	if code, _ := serveWithToken(t, token, http.MethodGet, "/api/vault/secrets/pki/./signing/private"); code != http.StatusOK {
		t.Fatalf("normalized in-subtree read = %d, want 200", code)
	}
}

// The gate reads r.URL.Path, which net/http has already percent-decoded, while
// chi routes on r.URL.RawPath and hands the vault handler the still-encoded
// wildcard — so for any path carrying an escape the two see different subjects.
// This pins that divergence and the gate's answer to it: refuse the whole
// class, since no vault path a service legitimately reads needs escaping.
func TestCapabilityGateRefusesPercentEncodedPaths(t *testing.T) {
	escaped := "/api/vault/secrets/projects/x%2Fcredentials%2Fadmin/credentials/auth_admin"
	req := httptest.NewRequest(http.MethodGet, escaped, nil)
	if req.URL.RawPath == "" || req.URL.RawPath == req.URL.Path {
		t.Fatalf("net/http no longer splits raw from decoded: Path=%q RawPath=%q", req.URL.Path, req.URL.RawPath)
	}

	token := &domain.AccessToken{Name: "svc-auth", Permissions: permsAuth}
	for _, path := range []string{
		escaped,
		// The escaped form of a path the token really is granted: still
		// refused, because the handler would receive the literal escapes.
		"/api/vault/secrets/projects/proj-1/credentials/auth%5Fadmin",
	} {
		if code, reached := serveWithToken(t, token, http.MethodGet, path); code != http.StatusForbidden || reached {
			t.Fatalf("%s: code=%d reached=%v, want 403", path, code, reached)
		}
	}
}

func TestCapabilityGateRefusesAnUnknownPermission(t *testing.T) {
	token := &domain.AccessToken{Name: "svc-broken", Permissions: []string{"not-a-capability"}}
	if code, _ := serveWithToken(t, token, http.MethodGet, "/api/projects/proj-1/info"); code != http.StatusForbidden {
		t.Fatalf("unparseable permission = %d, want 403", code)
	}
	// /me stays reachable so a service can still discover its identity.
	if code, _ := serveWithToken(t, token, http.MethodGet, "/api/auth/me"); code != http.StatusOK {
		t.Fatalf("self read = %d, want 200", code)
	}
}

// serveRequireCapability runs RequireCapability(want) in front of a handler
// that records that it ran.
func serveRequireCapability(want auth.Capability, token *domain.AccessToken) (int, bool) {
	reached := false
	handler := RequireCapability(want)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reached = true
		w.WriteHeader(http.StatusOK)
	}))
	req := httptest.NewRequest(http.MethodPost, "/internal/email/send", nil)
	if token != nil {
		req = req.WithContext(auth.SetToken(req.Context(), token))
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec.Code, reached
}

// A route that exists only for a service principal must refuse every caller
// that is not one — a human PAT or studio session reaching it would mean any
// logged-in user could drive the platform's outbound mail.
func TestRequireCapabilityAdmitsOnlyGrantedServiceTokens(t *testing.T) {
	want := EmailRelayCapability()
	cases := []struct {
		name  string
		token *domain.AccessToken
		code  int
	}{
		{name: "no token", token: nil, code: http.StatusForbidden},
		{name: "human PAT", token: &domain.AccessToken{Name: "ci"}, code: http.StatusForbidden},
		{name: "studio session", token: &domain.AccessToken{Name: "session", Scopes: "admin"}, code: http.StatusForbidden},
		{name: "service token without the capability", token: &domain.AccessToken{Name: "svc-graphql", Permissions: permsGraphql}, code: http.StatusForbidden},
		{name: "service token with the capability", token: &domain.AccessToken{Name: "svc-auth", Permissions: permsAuth}, code: http.StatusOK},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, reached := serveRequireCapability(want, tc.token)
			if code != tc.code || reached != (tc.code == http.StatusOK) {
				t.Fatalf("code=%d reached=%v, want %d", code, reached, tc.code)
			}
		})
	}
}
