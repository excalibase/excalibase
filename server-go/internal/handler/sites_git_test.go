package handler

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/excalibase/provisioning-poc/internal/auth"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/sitebuilds"
)

const studioOrigin = "https://studio.example.com"

func gitRouter(service *sitebuilds.Service) http.Handler {
	h := NewSiteGitHandler(service, studioOrigin)
	r := chi.NewRouter()
	r.Route("/api/projects/{projectId}/sites", func(r chi.Router) {
		h.Routes(r, func(next http.Handler) http.Handler { return next })
	})
	r.Route("/api/sites/github", h.PublicRoutes)
	return r
}

func gitRequest(method, path, body string) *http.Request {
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	return request.WithContext(auth.SetUser(context.Background(), &domain.User{ID: "u1", Active: true}))
}

// Off, every git route answers as if it were never mounted; the flag ships dark.
func TestSiteGit_OffIsNotFound(t *testing.T) {
	router := gitRouter(sitebuilds.NewService(nil, nil, nil, nil, nil, sitebuilds.Options{Mode: sitebuilds.ModeOff}))
	for _, route := range []struct{ method, path string }{
		{"GET", "/api/projects/p1/sites/s1/git"}, {"PUT", "/api/projects/p1/sites/s1/git"}, {"POST", "/api/projects/p1/sites/s1/builds"},
		{"GET", "/api/projects/p1/sites/s1/builds"}, {"GET", "/api/projects/p1/sites/build-secrets"},
		{"POST", "/api/sites/github/webhook"}, {"GET", "/api/sites/github/callback"},
	} {
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, gitRequest(route.method, route.path, "{}"))
		if recorder.Code != http.StatusNotFound {
			t.Errorf("%s %s = %d, want 404 while git builds are off", route.method, route.path, recorder.Code)
		}
	}
}

// Single-host says so by name, in the API as Sites does without Cloudflare.
func TestSiteGit_SingleHostSaysWhy(t *testing.T) {
	router := gitRouter(sitebuilds.NewService(nil, nil, nil, nil, nil, sitebuilds.Options{Mode: sitebuilds.ModeSingleHost}))
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, gitRequest("GET", "/api/projects/p1/sites/s1/git", ""))
	if recorder.Code != http.StatusServiceUnavailable || !strings.Contains(recorder.Body.String(), "single-host") {
		t.Fatalf("got %d %s", recorder.Code, recorder.Body.String())
	}
}

func TestSiteGit_RefusesABadSiteID(t *testing.T) {
	router := gitRouter(sitebuilds.NewService(nil, nil, nil, nil, nil, sitebuilds.Options{Mode: sitebuilds.ModeOn}))
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, gitRequest("GET", "/api/projects/p1/sites/bad$id/builds", ""))
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("got %d", recorder.Code)
	}
}

func TestSiteGitErrorStatuses(t *testing.T) {
	cases := map[error]int{
		sitebuilds.ErrNotConnected:            http.StatusNotFound,
		sitebuilds.ErrBuildNotFound:           http.StatusNotFound,
		sitebuilds.ErrNotAuthorized:           http.StatusForbidden,
		sitebuilds.ErrNotDeveloper:            http.StatusForbidden,
		sitebuilds.ErrStateInvalid:            http.StatusBadRequest,
		sitebuilds.ErrSingleHost:              http.StatusServiceUnavailable,
		&sitebuilds.InputError{Reason: "bad"}: http.StatusBadRequest,
		sitebuilds.ErrConnectionInactive:      http.StatusConflict,
	}
	for err, want := range cases {
		recorder := httptest.NewRecorder()
		writeSiteGitError(recorder, err)
		if recorder.Code != want {
			t.Errorf("%v: got %d, want %d", err, recorder.Code, want)
		}
	}
}

// An inactive connection answers why, so Studio and MCP can tell the user to reconnect.
func TestSiteGitError_InactiveConnectionSaysWhy(t *testing.T) {
	recorder := httptest.NewRecorder()
	writeSiteGitError(recorder, fmt.Errorf("rebuild: %w", sitebuilds.ErrConnectionInactive))
	if recorder.Code != http.StatusConflict || !strings.Contains(recorder.Body.String(), "can no longer deploy") {
		t.Fatalf("got %d %s", recorder.Code, recorder.Body.String())
	}
}

// The git routes sit beside the site tree on the same router; each reaches its own handler.
func TestSiteGit_RoutesBesideTheSiteTree(t *testing.T) {
	git := NewSiteGitHandler(sitebuilds.NewService(nil, nil, nil, nil, nil, sitebuilds.Options{Mode: sitebuilds.ModeSingleHost}), "")
	r := chi.NewRouter()
	r.Route("/api/projects/{projectId}/sites", func(r chi.Router) {
		pass := func(next http.Handler) http.Handler { return next }
		NewSiteHandler(nil).Routes(r, pass)
		git.Routes(r, pass)
	})
	for _, path := range []string{"/api/projects/p1/sites/s1/git", "/api/projects/p1/sites/s1/builds", "/api/projects/p1/sites/s1/builds/b1",
		"/api/projects/p1/sites/build-secrets"} {
		recorder := httptest.NewRecorder()
		r.ServeHTTP(recorder, gitRequest("GET", path, ""))
		if recorder.Code != http.StatusServiceUnavailable || !strings.Contains(recorder.Body.String(), "single-host") {
			t.Errorf("GET %s = %d %s; want the git handler's answer", path, recorder.Code, recorder.Body.String())
		}
	}
}

// The browser key rides only to the callback path, unreadable by scripts.
func TestSiteGitCookie(t *testing.T) {
	cookie := siteGitCookie("key", 600)
	if !cookie.HttpOnly || !cookie.Secure || cookie.SameSite != http.SameSiteLaxMode || cookie.Path != "/api/sites/github" {
		t.Fatalf("cookie %+v", cookie)
	}
}

// A callback with a state but without the starting browser's key is refused before anything is granted.
func TestSiteGit_CallbackWithoutTheBrowserKey(t *testing.T) {
	router := gitRouter(sitebuilds.NewService(nil, nil, nil, nil, nil, sitebuilds.Options{Mode: sitebuilds.ModeOn}))
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, gitRequest("GET", "/api/sites/github/callback?state=s&code=", ""))
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("got %d", recorder.Code)
	}
}
