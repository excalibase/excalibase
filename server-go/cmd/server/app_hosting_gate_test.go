package main

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/testutil/fakestore"
)

// EXC-377: app hosting is built incrementally on main and must stay
// unreachable until the whole epic ships. These tests pin that the gate is
// the router mount itself — a disabled install answers exactly the 404 a
// path that never existed would (EXC-436), not a 403 or an empty list.
func appHostingRouter(t *testing.T, enabled bool) http.Handler {
	t.Helper()
	instances := fakestore.NewInstances()
	instances.Create(&domain.DatabaseInstance{ProjectID: matrixProjectA, OrgID: matrixOrgA, Status: "ACTIVE"})
	platform := &fakePlatform{Orgs: fakestore.NewOrgs(), Tokens: fakestore.NewTokens()}
	cfg := config.AppConfig{DeploymentMode: "selfhosted", AppHostingEnabled: enabled}
	return buildRouter(cfg, platform, instances, matrixDeps(t, instances))
}

func TestAppHostingDisabled_AppsRouteAnswers404(t *testing.T) {
	router := appHostingRouter(t, false)
	req := httptest.NewRequest(http.MethodGet, "/api/projects/"+matrixProjectA+"/apps/", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("hosting disabled: got %d, want 404", w.Code)
	}
}

func TestAppHostingEnabled_AppsRouteIsMounted(t *testing.T) {
	router := appHostingRouter(t, true)
	req := httptest.NewRequest(http.MethodGet, "/api/projects/"+matrixProjectA+"/apps/", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code == http.StatusNotFound {
		t.Fatalf("hosting enabled: got 404, the route must be mounted")
	}
}

func TestAppHostingDisabled_DeployRoutesAnswer404(t *testing.T) {
	router := appHostingRouter(t, false)
	for _, req := range []*http.Request{
		httptest.NewRequest(http.MethodPost, "/api/projects/"+matrixProjectA+"/apps/app-1/deploy", nil),
		httptest.NewRequest(http.MethodGet, "/api/projects/"+matrixProjectA+"/apps/app-1/deploys", nil),
	} {
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		if w.Code != http.StatusNotFound {
			t.Fatalf("%s %s: hosting disabled: got %d, want 404", req.Method, req.URL.Path, w.Code)
		}
	}
}

func TestAppHostingEnabled_DeployRoutesAreMounted(t *testing.T) {
	router := appHostingRouter(t, true)
	req := httptest.NewRequest(http.MethodGet, "/api/projects/"+matrixProjectA+"/apps/app-1/deploys", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code == http.StatusNotFound {
		t.Fatalf("hosting enabled: got 404, the deploys route must be mounted")
	}
}
