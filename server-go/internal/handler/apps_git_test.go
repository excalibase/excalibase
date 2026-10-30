package handler

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/excalibase/provisioning-poc/internal/apphost"
	"github.com/excalibase/provisioning-poc/internal/sitebuilds"
)

// darkGate stands in for features.Require: off, it answers as an unmounted route.
func darkGate(on bool) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		if on {
			return next
		}
		return http.NotFoundHandler()
	}
}

func appGitRouter(service *sitebuilds.Service, flagOn bool) http.Handler {
	h := NewAppGitHandler(service)
	r := chi.NewRouter()
	r.Route("/api/projects/{projectId}/apps/{appId}", func(r chi.Router) {
		h.Routes(r, func(next http.Handler) http.Handler { return next }, darkGate(flagOn))
	})
	return r
}

var appGitRoutes = []struct{ method, path string }{
	{"GET", "/api/projects/p1/apps/a1/git"}, {"PUT", "/api/projects/p1/apps/a1/git"},
	{"POST", "/api/projects/p1/apps/a1/git/sign-in"}, {"GET", "/api/projects/p1/apps/a1/git/repositories"},
	{"GET", "/api/projects/p1/apps/a1/builds"}, {"POST", "/api/projects/p1/apps/a1/builds"},
	{"GET", "/api/projects/p1/apps/a1/builds/b1"},
}

// With the flag off every app git route but the disconnect answers as never mounted.
func TestAppGit_FlagOffIsNotFoundButDisconnectAnswers(t *testing.T) {
	router := appGitRouter(sitebuilds.NewService(nil, nil, nil, nil, nil, sitebuilds.Options{Mode: sitebuilds.ModeSingleHost}), false)
	for _, route := range appGitRoutes {
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, gitRequest(route.method, route.path, "{}"))
		if recorder.Code != http.StatusNotFound || recorder.Body.String() != "404 page not found\n" {
			t.Errorf("%s %s = %d %q, want the unmounted answer", route.method, route.path, recorder.Code, recorder.Body.String())
		}
	}
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, gitRequest("DELETE", "/api/projects/p1/apps/a1/git", ""))
	if recorder.Code == http.StatusNotFound {
		t.Fatalf("the disconnect reaches the service with the flag off: %d %s", recorder.Code, recorder.Body.String())
	}
}

// Git builds off for the install hide the app routes too; only the disconnect still reaches the service.
func TestAppGit_SiteGitBuildsOffIsNotFound(t *testing.T) {
	router := appGitRouter(sitebuilds.NewService(nil, nil, nil, nil, nil, sitebuilds.Options{Mode: sitebuilds.ModeOff}), true)
	for _, route := range appGitRoutes {
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, gitRequest(route.method, route.path, "{}"))
		if recorder.Code != http.StatusNotFound {
			t.Errorf("%s %s = %d, want 404", route.method, route.path, recorder.Code)
		}
	}
}

// With the flag on but nothing wired for apps here, the API says so by name.
func TestAppGit_UnwiredAppsSayWhy(t *testing.T) {
	router := appGitRouter(sitebuilds.NewService(nil, nil, nil, nil, nil, sitebuilds.Options{Mode: sitebuilds.ModeOn}), true)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, gitRequest("GET", "/api/projects/p1/apps/a1/builds", ""))
	if recorder.Code != http.StatusServiceUnavailable || !strings.Contains(recorder.Body.String(), "apps") {
		t.Fatalf("got %d %s", recorder.Code, recorder.Body.String())
	}
}

func TestAppGit_RefusesABadID(t *testing.T) {
	router := appGitRouter(sitebuilds.NewService(nil, nil, nil, nil, nil, sitebuilds.Options{Mode: sitebuilds.ModeOn}), true)
	for _, path := range []string{"/api/projects/p1/apps/bad$id/builds", "/api/projects/p1/apps/a1/builds/bad$id"} {
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, gitRequest("GET", path, ""))
		if recorder.Code != http.StatusBadRequest {
			t.Errorf("%s: got %d", path, recorder.Code)
		}
	}
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, gitRequest("PUT", "/api/projects/p1/apps/a1/git", "not json"))
	if recorder.Code != http.StatusBadRequest {
		t.Errorf("a body that is not JSON: %d", recorder.Code)
	}
}

func TestAppGitErrorStatuses(t *testing.T) {
	cases := map[error]int{
		apphost.ErrAppNotFound:                                http.StatusNotFound,
		sitebuilds.ErrAppBuildsOff:                            http.StatusServiceUnavailable,
		sitebuilds.ErrNotDeveloper:                            http.StatusForbidden,
		sitebuilds.ErrNotConnected:                            http.StatusNotFound,
		fmt.Errorf("x: %w", sitebuilds.ErrConnectionInactive): http.StatusConflict,
	}
	for err, want := range cases {
		recorder := httptest.NewRecorder()
		writeAppGitError(recorder, err)
		if recorder.Code != want {
			t.Errorf("%v: got %d, want %d", err, recorder.Code, want)
		}
	}
	recorder := httptest.NewRecorder()
	writeAppGitError(recorder, &sitebuilds.AppBuildRateLimitError{RetryAfter: 90*time.Second + time.Millisecond})
	if recorder.Code != http.StatusTooManyRequests || recorder.Header().Get("Retry-After") != "91" {
		t.Errorf("rate limited: %d %q", recorder.Code, recorder.Header().Get("Retry-After"))
	}
}

// The callback sends the browser back to the page the sign-in started from.
func TestSignInReturn_ToTheSiteOrTheApp(t *testing.T) {
	signedIn := url.Values{"git": {"signed-in"}}
	app := signInReturn(studioOrigin, sitebuilds.SignIn{ProjectID: "p 1", AppID: "a1"}, signedIn)
	if app != studioOrigin+"/project/p%201/containers/a1?git=signed-in" {
		t.Errorf("app: %s", app)
	}
	site := signInReturn(studioOrigin, sitebuilds.SignIn{ProjectID: "p1", SiteID: "s1"}, signedIn)
	if site != studioOrigin+"/project/p1/sites/s1?git=signed-in" {
		t.Errorf("site: %s", site)
	}
}
