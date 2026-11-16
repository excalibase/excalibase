package main

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/adminaccess"
	"github.com/excalibase/provisioning-poc/internal/config"
	custommw "github.com/excalibase/provisioning-poc/internal/middleware"
)

const (
	testAdminHost  = "admin.example.test"
	validAssertion = "valid-assertion"
)

type assertionChecker struct{}

func (assertionChecker) Verify(assertion string) error {
	if assertion == validAssertion {
		return nil
	}
	return errors.New("not an Access assertion")
}

func adminHostRouter(t *testing.T, cfg config.AppConfig) (http.Handler, callers) {
	t.Helper()
	cfg.DeploymentMode = "cloud"
	cfg.AdminHost = testAdminHost
	router, _, admins := buildMatrixWith(t, cfg, func(deps *handlerDeps) { deps.adminAssertions = assertionChecker{} })
	return router, admins
}

func adminCall(router http.Handler, host, token, assertion string) int {
	req := httptest.NewRequest(http.MethodGet, "/api/admin/console/overview", nil)
	req.Host = host
	req.Header.Set("Authorization", "Bearer "+token)
	if assertion != "" {
		req.Header.Set(adminaccess.AssertionHeader, assertion)
	}
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	return w.Code
}

// EXC-757: the console is answered on its own host only, and there only for a
// request Cloudflare Access vouches for; the platform_admin check stays on top.
func TestAdminHost_FencesTheAdminApi(t *testing.T) {
	router, admins := adminHostRouter(t, config.AppConfig{})
	session := admins[callerAdminSession]
	cases := []struct {
		name, host, assertion string
		want                  int
	}{
		{"another host looks unmounted", "app.example.test", validAssertion, http.StatusNotFound},
		{"the admin host without an assertion", testAdminHost, "", http.StatusForbidden},
		{"the admin host with a bad assertion", testAdminHost, "forged", http.StatusForbidden},
		{"the admin host with a valid assertion reaches the handler", testAdminHost, validAssertion, http.StatusOK},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := adminCall(router, tc.host, session, tc.assertion); got != tc.want {
				t.Fatalf("got %d want %d", got, tc.want)
			}
		})
	}
}

func TestAdminHost_AValidAssertionDoesNotReplaceTheRole(t *testing.T) {
	router, who, _ := buildMatrixWith(t, config.AppConfig{DeploymentMode: "cloud", AdminHost: testAdminHost}, func(deps *handlerDeps) { deps.adminAssertions = assertionChecker{} })
	// A customer's session, with a valid Access assertion, is still refused.
	if got := adminCall(router, testAdminHost, who[callerDeveloper], validAssertion); got != http.StatusForbidden {
		t.Fatalf("a developer got %d, want 403", got)
	}
}

// The in-cluster listener is not a way around the fence: the console is
// refused over the service address like it is on any non-admin host.
func TestAdminHost_TheConsoleIsNotReachableOverTheInClusterListener(t *testing.T) {
	router, admins := adminHostRouter(t, config.AppConfig{})
	req := httptest.NewRequest(http.MethodGet, "/api/admin/console/overview", nil)
	req.Host = "provisioning.excalibase.svc:8080"
	req.Header.Set("Authorization", "Bearer "+admins[callerAdminSession])
	w := httptest.NewRecorder()
	custommw.MarkInternalListener(router).ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("got %d, want 404", w.Code)
	}
}

// A cloud install fails closed (EXC-757): with a host but without Access it
// refuses to boot; with no host it boots and serves no admin console.
func TestAdminHost_ACloudHostWithoutAccessRefusesToBoot(t *testing.T) {
	t.Setenv("DEPLOYMENT_MODE", "cloud")
	t.Setenv("ADMIN_HOST", "admin.example.test")
	t.Setenv("ADMIN_ACCESS_TEAM_DOMAIN", "")
	t.Setenv("ADMIN_ACCESS_AUD", "")
	if err := config.Load().CheckAdminHost(); err == nil {
		t.Fatal("a cloud install booted with an admin host and no Access settings")
	}
	t.Setenv("ADMIN_ACCESS_TEAM_DOMAIN", "team.cloudflareaccess.com")
	t.Setenv("ADMIN_ACCESS_AUD", "aud-tag")
	if err := config.Load().CheckAdminHost(); err != nil {
		t.Fatalf("a complete fence was refused: %v", err)
	}
}

func TestAdminHost_ACloudInstallWithNoHostBootsWithNoConsole(t *testing.T) {
	t.Setenv("DEPLOYMENT_MODE", "cloud")
	t.Setenv("ADMIN_HOST", "")
	t.Setenv("ADMIN_ACCESS_TEAM_DOMAIN", "")
	t.Setenv("ADMIN_ACCESS_AUD", "")
	cfg := config.Load()
	if err := cfg.CheckAdminHost(); err != nil {
		t.Fatalf("a cloud install with no admin host must boot: %v", err)
	}
	router, _, admins := buildMatrixWith(t, cfg, func(*handlerDeps) {})
	for _, host := range []string{"app.example.test", "admin.example.test", "localhost"} {
		for _, path := range []string{"/api/admin/console/overview", "/api/admin/projects", "/api/admin/tiers/free"} {
			req := httptest.NewRequest(http.MethodGet, path, nil)
			req.Host = host
			req.Header.Set("Authorization", "Bearer "+admins[callerAdminSession])
			w := httptest.NewRecorder()
			router.ServeHTTP(w, req)
			if w.Code != http.StatusNotFound {
				t.Errorf("host %q %s: got %d, want 404 (no console)", host, path, w.Code)
			}
		}
	}
}

func TestAdminHost_SelfHostedStillBootsWithoutTheFence(t *testing.T) {
	t.Setenv("DEPLOYMENT_MODE", "selfhosted")
	t.Setenv("ADMIN_HOST", "")
	t.Setenv("ADMIN_ACCESS_TEAM_DOMAIN", "")
	t.Setenv("ADMIN_ACCESS_AUD", "")
	if err := config.Load().CheckAdminHost(); err != nil {
		t.Fatalf("self-hosted refused: %v", err)
	}
	router, _, admins := buildMatrixWith(t, config.AppConfig{DeploymentMode: "selfhosted"}, func(*handlerDeps) {})
	if got := adminCall(router, "app.example.test", admins[callerAdminSession], ""); got != http.StatusOK {
		t.Fatalf("got %d, want 200", got)
	}
}
