package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/auth"
	"github.com/excalibase/provisioning-poc/internal/domain"
)

// permsBootstrap is the svc-bootstrap list the chart builds (EXC-485): manage
// the two service principals' tokens, initialize the vault, and the union of
// the two lists it mints so the subset rule has something to subset.
var permsBootstrap = append([]string{
	"service-tokens:manage:svc-auth",
	"service-tokens:manage:svc-graphql",
	"vault:init",
}, append(append([]string{}, permsAuth...), permsGraphql...)...)

func TestRequiredCapabilityForTheBootstrapSurface(t *testing.T) {
	cases := []struct {
		method, path, want string
		found              bool
	}{
		{http.MethodPost, "/api/vault/init", "vault:init", true},
		{http.MethodPost, "/api/vault/unseal", "vault:unseal", true},
		{http.MethodGet, "/api/admin/service-accounts/svc-auth/tokens", "service-tokens:manage:svc-auth", true},
		{http.MethodGet, "/api/admin/service-accounts/svc-auth/tokens/", "service-tokens:manage:svc-auth", true},
		{http.MethodGet, "/api/admin/service-accounts", "", false},
		{http.MethodGet, "/api/admin/service-accounts/svc-auth", "", false},
		{http.MethodGet, "/api/vault/init", "", false},
	}
	for _, tc := range cases {
		got, ok := RequiredCapability(tc.method, tc.path)
		if ok != tc.found || (ok && got.String() != tc.want) {
			t.Errorf("RequiredCapability(%s %s) = %q,%v; want %q,%v", tc.method, tc.path, got.String(), ok, tc.want, tc.found)
		}
	}
}

func TestCapabilityGateAdmitsTheBootstrapTokenOnlyWhereItWorks(t *testing.T) {
	token := &domain.AccessToken{Name: "svc-bootstrap", Permissions: permsBootstrap}
	cases := []struct {
		method, path string
		admitted     bool
	}{
		// Service-token management; the handlers bind the principal.
		{http.MethodPost, "/api/admin/service-accounts", true},
		{http.MethodGet, "/api/admin/service-accounts/svc-auth/tokens", true},
		{http.MethodGet, "/api/admin/service-accounts/svc-graphql/tokens", true},
		{http.MethodPost, "/api/auth/tokens", true},
		{http.MethodDelete, "/api/auth/tokens/0123abcd", true},
		{http.MethodPost, "/api/auth/tokens/0123abcd/rotate", true},
		{http.MethodPost, "/api/vault/init", true},
		{http.MethodGet, "/api/vault/secrets/pki/signing/private", true},

		{http.MethodGet, "/api/admin/service-accounts/svc-bootstrap/tokens", false},
		{http.MethodGet, "/api/admin/service-accounts", false},
		{http.MethodDelete, "/api/admin/service-accounts/svc-auth", false},
		{http.MethodPost, "/api/vault/unseal", false},
		{http.MethodPost, "/api/vault/seal", false},
		{http.MethodPost, "/api/vault/rekey", false},
		{http.MethodPut, "/api/vault/secrets/pki/signing/private", false},
		{http.MethodGet, "/api/auth/tokens", false},
		{http.MethodGet, "/api/auth/users", false},
		{http.MethodPut, "/api/admin/tiers/FREE", false},
		{http.MethodGet, "/api/orgs", false},
	}
	for _, tc := range cases {
		code, reached := serveWithToken(t, token, tc.method, tc.path)
		if reached != tc.admitted {
			t.Errorf("svc-bootstrap %s %s: reached=%v code=%d, want admitted=%v", tc.method, tc.path, reached, code, tc.admitted)
		}
	}
}

func TestCapabilityGateKeepsServiceTokenManagementFromOtherServices(t *testing.T) {
	for _, perms := range [][]string{permsAuth, permsGraphql} {
		token := &domain.AccessToken{Name: "svc", Permissions: perms}
		for _, route := range []struct{ method, path string }{
			{http.MethodPost, "/api/auth/tokens"},
			{http.MethodDelete, "/api/auth/tokens/0123abcd"},
			{http.MethodPost, "/api/auth/tokens/0123abcd/rotate"},
			{http.MethodPost, "/api/admin/service-accounts"},
			{http.MethodPost, "/api/vault/init"},
		} {
			if _, reached := serveWithToken(t, token, route.method, route.path); reached {
				t.Errorf("%v reached %s %s", perms, route.method, route.path)
			}
		}
	}
}

func TestUnlessGrantedCapability(t *testing.T) {
	refuse := func(http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusForbidden) })
	}
	serve := func(token *domain.AccessToken, method, path string) int {
		handler := UnlessGrantedCapability(refuse)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		}))
		req := httptest.NewRequest(method, path, nil)
		if token != nil {
			req = req.WithContext(auth.SetToken(req.Context(), token))
		}
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec.Code
	}
	bootstrap := &domain.AccessToken{Permissions: []string{"vault:init"}}
	if code := serve(bootstrap, http.MethodPost, "/api/vault/init"); code != http.StatusOK {
		t.Fatalf("granted capability: %d, want 200", code)
	}
	if code := serve(bootstrap, http.MethodPost, "/api/vault/unseal"); code != http.StatusForbidden {
		t.Fatalf("capability not granted: %d, want the guard's 403", code)
	}
	if code := serve(&domain.AccessToken{Name: "pat"}, http.MethodPost, "/api/vault/init"); code != http.StatusForbidden {
		t.Fatalf("a human PAT must still meet the guard: %d", code)
	}
}
