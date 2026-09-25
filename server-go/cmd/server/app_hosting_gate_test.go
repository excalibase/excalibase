package main

import (
	"encoding/json"
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
		httptest.NewRequest(http.MethodPost, "/api/projects/"+matrixProjectA+"/apps/app-1/deploys/dep-1/redeploy", nil),
		httptest.NewRequest(http.MethodPut, "/api/projects/"+matrixProjectA+"/apps/app-1/secrets/API_KEY", nil),
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

func TestAppHostingEnabled_RedeployRouteIsMounted(t *testing.T) {
	router := appHostingRouter(t, true)
	req := httptest.NewRequest(http.MethodPost, "/api/projects/"+matrixProjectA+"/apps/app-1/deploys/dep-1/redeploy", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code == http.StatusNotFound {
		t.Fatalf("hosting enabled: got 404, the redeploy route must be mounted")
	}
}

func readConfigAppHosting(t *testing.T, enabled bool) bool {
	t.Helper()
	router := appHostingRouter(t, enabled)
	req := httptest.NewRequest(http.MethodGet, "/api/config", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("GET /api/config: got %d, want 200", w.Code)
	}
	var body struct {
		DeploymentMode string `json:"deploymentMode"`
		AppHosting     *bool  `json:"appHosting"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode /api/config: %v", err)
	}
	if body.DeploymentMode != "selfhosted" {
		t.Fatalf("deploymentMode: got %q, want selfhosted", body.DeploymentMode)
	}
	if body.AppHosting == nil {
		t.Fatal("appHosting is missing from /api/config")
	}
	return *body.AppHosting
}

func TestConfigReportsAppHostingEnabled(t *testing.T) {
	if !readConfigAppHosting(t, true) {
		t.Fatal("hosting enabled: /api/config reported appHosting=false")
	}
}

func TestConfigReportsAppHostingDisabled(t *testing.T) {
	if readConfigAppHosting(t, false) {
		t.Fatal("hosting disabled: /api/config reported appHosting=true")
	}
}

func TestAppHostingEnabled_SecretRouteIsMounted(t *testing.T) {
	router := appHostingRouter(t, true)
	req := httptest.NewRequest(http.MethodPut, "/api/projects/"+matrixProjectA+"/apps/app-1/secrets/API_KEY", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code == http.StatusNotFound {
		t.Fatalf("hosting enabled: got 404, the secret route must be mounted")
	}
}
