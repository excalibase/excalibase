//go:build integration

package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/auth"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/sitebuilds"
	"github.com/excalibase/provisioning-poc/internal/testutil/githubfake"
	"github.com/excalibase/provisioning-poc/internal/testutil/sitebuildstest"
)

type gitAPI struct {
	t       *testing.T
	harness *sitebuildstest.Harness
	router  http.Handler
}

func newGitAPI(t *testing.T, options sitebuildstest.Options) *gitAPI {
	harness := sitebuildstest.New(t, options)
	return &gitAPI{t: t, harness: harness, router: gitRouter(harness.Service)}
}

func (a *gitAPI) sitePath(rest string) string {
	return "/api/projects/" + a.harness.Project + "/sites/" + a.harness.Site.ID + rest
}

// call sends a request as the harness user, with any cookies given.
func (a *gitAPI) call(method, path, body string, cookies ...*http.Cookie) *httptest.ResponseRecorder {
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

func decodeGit[T any](t *testing.T, recorder *httptest.ResponseRecorder) T {
	t.Helper()
	var value T
	if err := json.Unmarshal(recorder.Body.Bytes(), &value); err != nil {
		t.Fatalf("decode %s: %v", recorder.Body.String(), err)
	}
	return value
}

// signIn runs the sign-in through the routes: the start sets the browser cookie, the callback needs it.
func (a *gitAPI) signIn() {
	a.t.Helper()
	started := a.call(http.MethodPost, a.sitePath("/git/sign-in"), `{"install":false}`)
	if started.Code != http.StatusOK {
		a.t.Fatalf("sign-in start: %d %s", started.Code, started.Body.String())
	}
	cookies := started.Result().Cookies()
	if len(cookies) != 1 || cookies[0].Name != siteGitBrowserCookie || !cookies[0].HttpOnly {
		a.t.Fatalf("cookies %+v", cookies)
	}
	link, err := url.Parse(decodeGit[map[string]string](a.t, started)["url"])
	if err != nil {
		a.t.Fatal(err)
	}
	a.harness.GitHub.AddSignIn("http-code", map[int64][]int64{a.harness.Installation: {a.harness.SiteRepo}})
	callback := "/api/sites/github/callback?" + url.Values{"state": {link.Query().Get("state")}, "code": {"http-code"}}.Encode()
	finished := a.call(http.MethodGet, callback, "", cookies[0])
	want := studioOrigin + "/project/" + a.harness.Project + "/sites/" + a.harness.Site.ID + "?git=signed-in"
	if finished.Code != http.StatusFound || finished.Header().Get("Location") != want {
		a.t.Fatalf("callback: %d %s", finished.Code, finished.Header().Get("Location"))
	}
}

func TestSiteGitRoutes_ConnectBuildAndRead(t *testing.T) {
	api := newGitAPI(t, sitebuildstest.Options{Secrets: map[string]string{"STRIPE_PK": "pk_live_x"}})
	if state := decodeGit[map[string]any](t, api.call(http.MethodGet, api.sitePath("/git"), "")); state["connected"] != false {
		t.Fatalf("before connecting: %v", state)
	}
	if refused := api.call(http.MethodPut, api.sitePath("/git"), `{"repository":"acme/site","settings":{"outputDir":"dist"}}`); refused.Code != http.StatusForbidden {
		t.Fatalf("connect before signing in: %d", refused.Code)
	}
	api.signIn()
	repositories := decodeGit[[]map[string]any](t, api.call(http.MethodGet, api.sitePath("/git/repositories"), ""))
	if len(repositories) != 1 {
		t.Fatalf("repositories %v", repositories)
	}
	connected := api.call(http.MethodPut, api.sitePath("/git"), `{"repository":"acme/site","settings":{"outputDir":"dist","env":{"VITE_A":"1"}}}`)
	if connected.Code != http.StatusOK || !strings.Contains(connected.Body.String(), `"build"`) {
		t.Fatalf("connect: %d %s", connected.Code, connected.Body.String())
	}
	if bad := api.call(http.MethodPut, api.sitePath("/git"), `{"repository":"acme/site","settings":{"outputDir":"../up"}}`); bad.Code != http.StatusBadRequest {
		t.Errorf("a climbing output dir: %d", bad.Code)
	}
	api.harness.RunAll()
	builds := decodeGit[[]sitebuilds.Build](t, api.call(http.MethodGet, api.sitePath("/builds"), ""))
	if len(builds) != 1 || builds[0].Status != sitebuilds.StatusSucceeded {
		t.Fatalf("builds %+v", builds)
	}
	one := decodeGit[sitebuilds.Build](t, api.call(http.MethodGet, api.sitePath("/builds/"+builds[0].ID), ""))
	if one.ID != builds[0].ID || one.Log == "" {
		t.Errorf("build %+v", one)
	}
	if missing := api.call(http.MethodGet, api.sitePath("/builds/00000000-0000-0000-0000-000000000000"), ""); missing.Code != http.StatusNotFound {
		t.Errorf("a missing build: %d", missing.Code)
	}
	if rebuilt := api.call(http.MethodPost, api.sitePath("/builds"), ""); rebuilt.Code != http.StatusAccepted {
		t.Errorf("rebuild: %d %s", rebuilt.Code, rebuilt.Body.String())
	}
	if disconnected := api.call(http.MethodDelete, api.sitePath("/git"), ""); disconnected.Code != http.StatusNoContent {
		t.Errorf("disconnect: %d", disconnected.Code)
	}
	if again := api.call(http.MethodPost, api.sitePath("/builds"), ""); again.Code != http.StatusNotFound {
		t.Errorf("rebuild after disconnect: %d", again.Code)
	}
}

func TestSiteGitRoutes_BuildSecrets(t *testing.T) {
	api := newGitAPI(t, sitebuildstest.Options{Secrets: map[string]string{"STRIPE_PK": "pk_live_x"}})
	secretsPath := "/api/projects/" + api.harness.Project + "/sites/build-secrets"
	listed := decodeGit[[]sitebuilds.SecretVisibility](t, api.call(http.MethodGet, secretsPath, ""))
	if len(listed) != 1 || listed[0].BuildVisible {
		t.Fatalf("listed %+v", listed)
	}
	set := decodeGit[[]sitebuilds.SecretVisibility](t, api.call(http.MethodPut, secretsPath, `{"buildVisible":["STRIPE_PK"]}`))
	if !set[0].BuildVisible {
		t.Errorf("set %+v", set)
	}
	if bad := api.call(http.MethodPut, secretsPath, `not json`); bad.Code != http.StatusBadRequest {
		t.Errorf("bad body: %d", bad.Code)
	}
}

func TestSiteGitRoutes_Webhook(t *testing.T) {
	api := newGitAPI(t, sitebuildstest.Options{})
	api.harness.Connect()
	sha := strings.Repeat("e", 40)
	api.harness.GitHub.SetBranchHead(api.harness.SiteRepo, "main", sha)
	body := githubfake.PushPayload(api.harness.Installation, api.harness.SiteRepo, "acme/site", "main", sha, "feat: hook")
	deliver := func(signature string) *httptest.ResponseRecorder {
		request := httptest.NewRequest(http.MethodPost, "/api/sites/github/webhook", strings.NewReader(string(body)))
		request.Header.Set("X-GitHub-Event", "push")
		request.Header.Set("X-GitHub-Delivery", "d-http")
		request.Header.Set("X-Hub-Signature-256", signature)
		recorder := httptest.NewRecorder()
		api.router.ServeHTTP(recorder, request)
		return recorder
	}
	if unsigned := deliver("sha256=00"); unsigned.Code != http.StatusUnauthorized {
		t.Errorf("unsigned: %d", unsigned.Code)
	}
	accepted := deliver(api.harness.GitHub.Sign(body))
	if accepted.Code != http.StatusAccepted || decodeGit[map[string]int](t, accepted)["queued"] != 1 {
		t.Errorf("signed: %d %s", accepted.Code, accepted.Body.String())
	}
}

func TestSiteGitRoutes_CallbackRefusals(t *testing.T) {
	api := newGitAPI(t, sitebuildstest.Options{})
	started := api.call(http.MethodPost, api.sitePath("/git/sign-in"), "")
	link, _ := url.Parse(decodeGit[map[string]string](t, started)["url"])
	// The link alone, opened in another browser, grants nothing.
	callback := "/api/sites/github/callback?" + url.Values{"state": {link.Query().Get("state")}, "code": {"lured"}}.Encode()
	if lured := api.call(http.MethodGet, callback, ""); lured.Code != http.StatusBadRequest {
		t.Errorf("without the browser cookie: %d", lured.Code)
	}
	if unknown := api.call(http.MethodPost, "/api/projects/"+api.harness.Project+"/sites/no-such-site/git/sign-in", ""); unknown.Code != http.StatusNotFound {
		t.Errorf("a site of no project: %d", unknown.Code)
	}
}
