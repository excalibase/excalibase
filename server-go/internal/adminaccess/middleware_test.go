package adminaccess

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/golang-jwt/jwt/v5"
)

const adminHost = "admin.example.test"

func gate(f *accessFixture, withAccess bool) http.Handler {
	ok := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	cfg := GateConfig{AdminHost: adminHost}
	if withAccess {
		cfg.Verifier = f.verifier
	}
	return Gate(cfg)(ok)
}

func call(h http.Handler, host, path, assertion string) int {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.Host = host
	if assertion != "" {
		req.Header.Set("Cf-Access-Jwt-Assertion", assertion)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w.Code
}

func TestGate_TheAdminApiLooksUnmountedOnAnyOtherHost(t *testing.T) {
	f := newFixture(t)
	h := gate(f, true)
	good := f.token(t, nil)
	for _, host := range []string{"app.example.test", "admin.example.test.evil.test", ""} {
		if got := call(h, host, "/api/admin/console/users", good); got != http.StatusNotFound {
			t.Errorf("host %q: got %d, want 404", host, got)
		}
	}
	if got := call(h, "app.example.test", "/api/projects/p1", ""); got != http.StatusOK {
		t.Errorf("customer traffic on the app host must pass untouched, got %d", got)
	}
}

func TestGate_EveryAdminHostRequestNeedsAValidAccessAssertion(t *testing.T) {
	f := newFixture(t)
	h := gate(f, true)
	cases := []struct {
		name      string
		assertion string
		want      int
	}{
		{"valid", f.token(t, nil), http.StatusOK},
		{"missing", "", http.StatusForbidden},
		{"garbage", "abc.def.ghi", http.StatusForbidden},
		{"wrong audience", f.token(t, func(c jwt.MapClaims, _ map[string]any) { c["aud"] = []string{"other"} }), http.StatusForbidden},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := call(h, adminHost, "/api/admin/console/users", tc.assertion); got != tc.want {
				t.Fatalf("got %d want %d", got, tc.want)
			}
		})
	}
}

// The origin is reachable directly with a forged Host header; only the
// assertion proves the request came through Cloudflare Access.
func TestGate_AForgedHostHeaderAloneGetsNothing(t *testing.T) {
	f := newFixture(t)
	h := gate(f, true)
	for _, path := range []string{"/api/admin/projects", "/api/auth/login", "/api/config", "/"} {
		if got := call(h, adminHost, path, ""); got != http.StatusForbidden {
			t.Errorf("%s without an assertion: got %d, want 403", path, got)
		}
	}
}

func TestGate_TheAdminHostServesOnlyTheConsoleAndWhatItNeeds(t *testing.T) {
	f := newFixture(t)
	h := gate(f, true)
	good := f.token(t, nil)
	allowed := []string{"/api/admin/console/users", "/api/auth/me", "/api/config", "/api/capacity", "/api/vault/status", "/api/auth/setup-status", "/", "/assets/index.js", "/admin"}
	for _, path := range allowed {
		if got := call(h, adminHost, path, good); got != http.StatusOK {
			t.Errorf("%s: got %d, want 200", path, got)
		}
	}
	refused := []string{"/api/projects/p1/info", "/api/schema/p1/tables", "/api/provision", "/api/orgs/", "/functions/v1/p1/x", "/storage/v1/x", "/internal/email/send"}
	for _, path := range refused {
		if got := call(h, adminHost, path, good); got != http.StatusNotFound {
			t.Errorf("%s: got %d, want 404", path, got)
		}
	}
}

// Only a self-hosted install reaches this: a cloud install refuses to boot
// without the Access settings (config.CheckAdminHost), so the gate never runs
// open there.
func TestGate_SelfHostedWithoutAnAccessConfigHasTheHostAsItsFence(t *testing.T) {
	f := newFixture(t)
	h := gate(f, false)
	if got := call(h, adminHost, "/api/admin/console/users", ""); got != http.StatusOK {
		t.Errorf("admin host, no Access configured: got %d, want 200", got)
	}
	if got := call(h, "app.example.test", "/api/admin/console/users", ""); got != http.StatusNotFound {
		t.Errorf("app host: got %d, want 404", got)
	}
}

func TestGate_IsOffWithoutAnAdminHost(t *testing.T) {
	ok := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	h := Gate(GateConfig{})(ok)
	if got := call(h, "app.example.test", "/api/admin/console/users", ""); got != http.StatusOK {
		t.Fatalf("got %d, want the request to pass", got)
	}
}

// The in-cluster listener skips the gate for the service-account routes only,
// which the bootstrap Job reaches by service address. The console still needs
// its host and its Access assertion, whichever listener carried the request.
func TestGate_TheInClusterListenerSkipsTheGateForServiceAccountRoutesOnly(t *testing.T) {
	f := newFixture(t)
	ok := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	h := Gate(GateConfig{AdminHost: adminHost, Verifier: f.verifier, IsInternal: func(*http.Request) bool { return true }})(ok)
	if got := call(h, "provisioning.svc:8080", "/api/admin/service-accounts/", ""); got != http.StatusOK {
		t.Fatalf("service accounts: got %d, want 200", got)
	}
	for _, path := range []string{"/api/admin/console/users", "/api/admin/projects", "/api/admin/orgs/o1", "/api/admin/console/orgs/o1/tier"} {
		if got := call(h, "provisioning.svc:8080", path, ""); got != http.StatusNotFound {
			t.Errorf("%s over the service address: got %d, want 404", path, got)
		}
		if got := call(h, adminHost, path, ""); got != http.StatusForbidden {
			t.Errorf("%s on the admin host without an assertion, in-cluster listener: got %d, want 403", path, got)
		}
	}
}

// A cloud install with no admin host has no console: every admin route is
// unmounted on every host, the service-account routes excepted.
func TestGate_DisabledConsoleAnswers404EverywhereExceptServiceAccounts(t *testing.T) {
	ok := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	h := Gate(GateConfig{Disabled: true})(ok)
	for _, host := range []string{"app.example.test", "admin.example.test", ""} {
		for _, path := range []string{"/api/admin/console/users", "/api/admin/projects", "/api/admin/orgs/o1", "/api/admin/tiers/free", "/api/admin"} {
			if got := call(h, host, path, ""); got != http.StatusNotFound {
				t.Errorf("host %q %s: got %d, want 404", host, path, got)
			}
		}
		if got := call(h, host, "/api/admin/service-accounts/", ""); got != http.StatusOK {
			t.Errorf("host %q service accounts: got %d, want 200", host, got)
		}
		if got := call(h, host, "/api/projects/p1", ""); got != http.StatusOK {
			t.Errorf("host %q customer traffic: got %d, want 200", host, got)
		}
	}
}
