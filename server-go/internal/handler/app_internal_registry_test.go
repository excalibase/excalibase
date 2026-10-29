package handler

import (
	"net/http"
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/appregistry"
	"github.com/excalibase/provisioning-poc/internal/appregistry/appregistrytest"
	"github.com/excalibase/provisioning-poc/internal/features"
	"github.com/go-chi/chi/v5"
)

func setupAppRouterWithInternalRegistry(t *testing.T) chi.Router {
	t.Helper()
	store := newFakeAppStore()
	h := NewAppHandler(store, newFakeSources("storefront_db"), testAppRoute)
	h.SetFeatures(features.NewStatic(features.All()...))
	h.SetAppLimits(fakeAppLimits{limit: testAppLimit})
	h.SetInternalRegistry(appregistrytest.New(t))
	r := chi.NewRouter()
	r.Route("/api/projects/{projectId}/apps", func(r chi.Router) { h.Routes(r) })
	return r
}

// Only the platform's builds put an image on its registry; a person or a token naming one is refused (EXC-540).
func TestAppHandler_RefusesAnImageOnTheInternalRegistry(t *testing.T) {
	r := setupAppRouterWithInternalRegistry(t)
	body := validAppBody()
	body["image"] = "Registry.Excalibase.Internal/proj-other/api:main"
	w := doAppRequest(t, r, http.MethodPost, "/api/projects/"+appTestProject+"/apps/", body)
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "git builds") {
		t.Fatalf("create: %d %s", w.Code, w.Body)
	}
	created := createAppForTest(t, r)
	w = doAppRequest(t, r, http.MethodPatch, "/api/projects/"+appTestProject+"/apps/"+created.ID+"/",
		map[string]any{"image": appregistrytest.Host + "/" + appTestProject + "/storefront@sha256:" + strings.Repeat("a", 64)})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("update: %d %s", w.Code, w.Body)
	}
}

func TestAppDeployHandler_Deploy_RefusesTheInternalRegistry(t *testing.T) {
	deployer := newFakeAppDeployer()
	deployer.deployErr = appregistry.ErrInternalImage
	rec := doDeployRequest(t, setupAppDeployRouter(t, deployer), http.MethodPost, "/api/projects/"+deployHandlerProject+"/apps/app-1/deploy")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("got %d", rec.Code)
	}
}
