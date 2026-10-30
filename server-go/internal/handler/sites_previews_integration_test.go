//go:build integration

package handler

import (
	"net/http"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/sitebuilds"
	"github.com/excalibase/provisioning-poc/internal/sites"
	"github.com/excalibase/provisioning-poc/internal/testutil/sitebuildstest"
	"github.com/go-chi/chi/v5"
)

func previewRouterAll(service *sitebuilds.Service) http.Handler {
	h := NewSiteGitHandler(service, studioOrigin)
	pass := func(next http.Handler) http.Handler { return next }
	r := chi.NewRouter()
	r.Route("/api/projects/{projectId}/sites", func(r chi.Router) { h.PreviewRoutes(r, pass, pass) })
	return r
}

// The preview routes end to end over the real service and store: settings, list, remove.
func TestSitePreviews_SettingsListAndRemove(t *testing.T) {
	harness := sitebuildstest.New(t, sitebuildstest.Options{Previews: true})
	harness.Connect()
	harness.RunAll()
	api := &gitAPI{t: t, harness: harness, router: previewRouterAll(harness.Service)}

	if got := api.call(http.MethodPut, api.sitePath("/previews/mode"), `{"mode":"tags"}`); got.Code != http.StatusBadRequest {
		t.Fatalf("bad mode = %d %s", got.Code, got.Body.String())
	}
	set := api.call(http.MethodPut, api.sitePath("/previews/mode"), `{"mode":"branches"}`)
	if set.Code != http.StatusOK || decodeGit[sitebuilds.PreviewOverview](t, set).Settings.Mode != sitebuilds.PreviewModeBranches {
		t.Fatalf("set mode = %d %s", set.Code, set.Body.String())
	}
	access := api.call(http.MethodPut, api.sitePath("/previews/access"), `{"forks":false,"secrets":false,"apiAccess":true,"publicLinks":false}`)
	if access.Code != http.StatusOK || !decodeGit[sitebuilds.PreviewOverview](t, access).Settings.APIAccess {
		t.Fatalf("set access = %d %s", access.Code, access.Body.String())
	}
	if _, err := harness.Push("feat/a", "5555555555555555555555555555555555555555", "d-api-1"); err != nil {
		t.Fatal(err)
	}
	harness.RunAll()
	listed := decodeGit[sitebuilds.PreviewOverview](t, api.call(http.MethodGet, api.sitePath("/previews"), ""))
	if len(listed.Previews) != 1 || listed.Used != 1 || listed.Max != sites.DefaultLimits.MaxPreviews || listed.Previews[0].Status != sites.PreviewReady {
		t.Fatalf("listed = %+v", listed)
	}
	if got := api.call(http.MethodDelete, api.sitePath("/previews/"+listed.Previews[0].ID), ""); got.Code != http.StatusNoContent {
		t.Fatalf("remove = %d %s", got.Code, got.Body.String())
	}
	if got := api.call(http.MethodDelete, api.sitePath("/previews/"+listed.Previews[0].ID), ""); got.Code != http.StatusNotFound {
		t.Fatalf("remove again = %d %s", got.Code, got.Body.String())
	}
	if got := api.call(http.MethodGet, "/api/projects/other/sites/"+harness.Site.ID+"/previews", ""); got.Code != http.StatusNotFound {
		t.Fatalf("another project's site = %d", got.Code)
	}
}
