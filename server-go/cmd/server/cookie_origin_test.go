package main

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/auth"
	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/edgefn"
	"github.com/excalibase/provisioning-poc/internal/testutil/fakestore"
)

// A page beside Studio (a hosted app on a sibling subdomain is same-site, so
// SameSite=Strict still sends the cookie) must not act as the signed-in user.
func TestRouterRefusesCookieWritesFromOtherOrigins(t *testing.T) {
	instances := fakestore.NewInstances()
	platform := &fakePlatform{Orgs: fakestore.NewOrgs(), Tokens: fakestore.NewTokens()}
	deps := policyDeps(t, instances, platform, edgefn.NewFunctionStore(t.TempDir()))
	cfg := config.AppConfig{DeploymentMode: "cloud", StudioURL: "https://studio.example.test", CORSOrigins: []string{"*"}}
	router := buildRouter(cfg, platform, instances, deps)

	post := func(origin string) int {
		req := httptest.NewRequest(http.MethodPost, "/api/auth/logout", nil)
		req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: "session"})
		req.Header.Set("Origin", origin)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		return w.Code
	}
	if got := post("https://app.apps.example.test"); got != http.StatusForbidden {
		t.Fatalf("cookie write from a sibling page: got %d, want 403", got)
	}
	if got := post("https://studio.example.test"); got == http.StatusForbidden {
		t.Fatal("cookie write from Studio itself was refused")
	}
}
