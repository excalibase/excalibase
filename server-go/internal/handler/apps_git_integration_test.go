//go:build integration

package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/excalibase/provisioning-poc/internal/auth"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/sitebuilds"
	"github.com/excalibase/provisioning-poc/internal/testutil/sitebuildstest"
)

type appGitAPI struct {
	t       *testing.T
	harness *sitebuildstest.Harness
	router  http.Handler
	appID   string
}

func newAppGitAPI(t *testing.T) *appGitAPI {
	harness := sitebuildstest.New(t, sitebuildstest.Options{AppsOn: true})
	r := chi.NewRouter()
	pass := func(next http.Handler) http.Handler { return next }
	r.Route("/api/projects/{projectId}/apps/{appId}", func(r chi.Router) {
		NewAppGitHandler(harness.Service).Routes(r, pass, pass)
	})
	r.Route("/api/sites/github", NewSiteGitHandler(harness.Service, studioOrigin).PublicRoutes)
	return &appGitAPI{t: t, harness: harness, router: r, appID: harness.AddApp(harness.Project)}
}

func (a *appGitAPI) appPath(rest string) string {
	return "/api/projects/" + a.harness.Project + "/apps/" + a.appID + rest
}

func (a *appGitAPI) call(method, path, body string, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	a.t.Helper()
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	request = request.WithContext(auth.SetUser(context.Background(), &domain.User{ID: a.harness.User, Active: true}))
	for _, cookie := range cookies {
		request.AddCookie(cookie)
	}
	recorder := httptest.NewRecorder()
	a.router.ServeHTTP(recorder, request)
	return recorder
}

// signIn starts from the app and the callback sends the browser back to the app's page.
func (a *appGitAPI) signIn() {
	a.t.Helper()
	started := a.call(http.MethodPost, a.appPath("/git/sign-in"), "")
	if started.Code != http.StatusOK {
		a.t.Fatalf("sign-in start: %d %s", started.Code, started.Body.String())
	}
	cookies := started.Result().Cookies()
	link, err := url.Parse(decodeGit[map[string]string](a.t, started)["url"])
	if err != nil || len(cookies) != 1 {
		a.t.Fatalf("link %v cookies %+v", err, cookies)
	}
	a.harness.GitHub.AddSignIn("app-code", map[int64][]int64{a.harness.Installation: {a.harness.SiteRepo}})
	callback := "/api/sites/github/callback?" + url.Values{"state": {link.Query().Get("state")}, "code": {"app-code"}}.Encode()
	finished := a.call(http.MethodGet, callback, "", cookies[0])
	want := studioOrigin + "/project/" + a.harness.Project + "/containers/" + a.appID + "?git=signed-in"
	if finished.Code != http.StatusFound || finished.Header().Get("Location") != want {
		a.t.Fatalf("callback: %d %s", finished.Code, finished.Header().Get("Location"))
	}
}

func TestAppGitRoutes_ConnectBuildAndRead(t *testing.T) {
	api := newAppGitAPI(t)
	if state := decodeGit[map[string]any](t, api.call(http.MethodGet, api.appPath("/git"), "")); state["connected"] != false {
		t.Fatalf("before connecting: %v", state)
	}
	if refused := api.call(http.MethodPut, api.appPath("/git"), `{"repository":"acme/site"}`); refused.Code != http.StatusForbidden {
		t.Fatalf("connect before signing in: %d", refused.Code)
	}
	api.signIn()
	if repositories := decodeGit[[]map[string]any](t, api.call(http.MethodGet, api.appPath("/git/repositories"), "")); len(repositories) != 1 {
		t.Fatalf("repositories %v", repositories)
	}
	if bad := api.call(http.MethodPut, api.appPath("/git"), `{"repository":"acme/site","settings":{"rootDir":"../up"}}`); bad.Code != http.StatusBadRequest {
		t.Errorf("a climbing root dir: %d", bad.Code)
	}
	connected := api.call(http.MethodPut, api.appPath("/git"), `{"repository":"acme/site","settings":{"builder":"buildpacks"}}`)
	if connected.Code != http.StatusOK || !strings.Contains(connected.Body.String(), `"build"`) {
		t.Fatalf("connect: %d %s", connected.Code, connected.Body.String())
	}
	api.harness.RunAll()
	builds := decodeGit[[]sitebuilds.Build](t, api.call(http.MethodGet, api.appPath("/builds"), ""))
	if len(builds) != 1 || builds[0].Status != sitebuilds.StatusSucceeded || builds[0].AppID != api.appID {
		t.Fatalf("builds %+v", builds)
	}
	if one := decodeGit[sitebuilds.Build](t, api.call(http.MethodGet, api.appPath("/builds/"+builds[0].ID), "")); one.ID != builds[0].ID {
		t.Errorf("build %+v", one)
	}
	if missing := api.call(http.MethodGet, api.appPath("/builds/00000000-0000-0000-0000-000000000000"), ""); missing.Code != http.StatusNotFound {
		t.Errorf("a missing build: %d", missing.Code)
	}
	if rebuilt := api.call(http.MethodPost, api.appPath("/builds"), ""); rebuilt.Code != http.StatusAccepted {
		t.Errorf("rebuild: %d %s", rebuilt.Code, rebuilt.Body.String())
	}
	if disconnected := api.call(http.MethodDelete, api.appPath("/git"), ""); disconnected.Code != http.StatusNoContent {
		t.Errorf("disconnect: %d", disconnected.Code)
	}
	if again := api.call(http.MethodPost, api.appPath("/builds"), ""); again.Code != http.StatusNotFound {
		t.Errorf("rebuild after disconnect: %d", again.Code)
	}
}

func TestAppGitRoutes_AnAppOfNoProject(t *testing.T) {
	api := newAppGitAPI(t)
	other := "/api/projects/" + api.harness.Project + "/apps/no-such-app"
	for _, route := range []struct{ method, path string }{
		{http.MethodGet, other + "/git"}, {http.MethodPost, other + "/git/sign-in"}, {http.MethodGet, other + "/builds"},
	} {
		if got := api.call(route.method, route.path, ""); got.Code != http.StatusNotFound {
			t.Errorf("%s %s: %d", route.method, route.path, got.Code)
		}
	}
}
