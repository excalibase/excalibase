package handler

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/excalibase/provisioning-poc/internal/sitebuilds"
	"github.com/excalibase/provisioning-poc/internal/sites"
)

// previewRouter mounts the preview routes with gates that name themselves, so a test sees which rung each needs.
func previewRouter(service *sitebuilds.Service) http.Handler {
	h := NewSiteGitHandler(service, "")
	r := chi.NewRouter()
	gate := func(name string) func(http.Handler) http.Handler {
		return func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("X-Rung") != name && r.Header.Get("X-Rung") != "admin" {
					http.Error(w, "gate "+name, http.StatusForbidden)
					return
				}
				next.ServeHTTP(w, r)
			})
		}
	}
	r.Route("/api/projects/{projectId}/sites", func(r chi.Router) {
		h.PreviewRoutes(r, gate("developer"), gate("admin"))
	})
	return r
}

func previewRequest(method, path, rung string) *http.Request {
	request := httptest.NewRequest(method, path, strings.NewReader(`{"mode":"branches"}`))
	request.Header.Set("X-Rung", rung)
	return request
}

var previewRoutes = []struct{ method, path string }{
	{"GET", "/api/projects/p1/sites/s1/previews"}, {"PUT", "/api/projects/p1/sites/s1/previews/mode"},
	{"PUT", "/api/projects/p1/sites/s1/previews/access"}, {"DELETE", "/api/projects/p1/sites/s1/previews/v1"},
}

// With git builds on but no previews wired, every preview route answers as if it were never mounted.
func TestSitePreviews_DarkRoutes(t *testing.T) {
	router := previewRouter(sitebuilds.NewService(nil, nil, nil, nil, nil, sitebuilds.Options{Mode: sitebuilds.ModeOn}))
	for _, route := range previewRoutes {
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, previewRequest(route.method, route.path, "admin"))
		if recorder.Code != http.StatusNotFound || recorder.Body.String() != "404 page not found\n" {
			t.Errorf("%s %s = %d %q, want unmounted", route.method, route.path, recorder.Code, recorder.Body.String())
		}
	}
}

// Where git builds are not offered, the routes say why, as the git routes do.
func TestSitePreviews_SayWhyWithoutGitBuilds(t *testing.T) {
	for _, mode := range []sitebuilds.Mode{sitebuilds.ModeOff, sitebuilds.ModeSingleHost} {
		router := previewRouter(sitebuilds.NewService(nil, nil, nil, nil, nil, sitebuilds.Options{Mode: mode}))
		for _, route := range previewRoutes {
			recorder := httptest.NewRecorder()
			router.ServeHTTP(recorder, previewRequest(route.method, route.path, "admin"))
			if recorder.Code != http.StatusServiceUnavailable {
				t.Errorf("mode %d: %s %s = %d, want 503", mode, route.method, route.path, recorder.Code)
			}
		}
	}
}

// The opt-ins reach past the preview, so only an Admin changes them; a developer sets the mode.
func TestSitePreviews_AccessNeedsAdmin(t *testing.T) {
	router := previewRouter(sitebuilds.NewService(nil, nil, nil, nil, nil, sitebuilds.Options{Mode: sitebuilds.ModeOn}))
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, previewRequest("PUT", "/api/projects/p1/sites/s1/previews/access", "developer"))
	if recorder.Code != http.StatusForbidden || !strings.Contains(recorder.Body.String(), "gate admin") {
		t.Fatalf("developer on access = %d %s", recorder.Code, recorder.Body.String())
	}
	recorder = httptest.NewRecorder()
	router.ServeHTTP(recorder, previewRequest("PUT", "/api/projects/p1/sites/s1/previews/mode", "viewer"))
	if recorder.Code != http.StatusForbidden || !strings.Contains(recorder.Body.String(), "gate developer") {
		t.Fatalf("viewer on mode = %d %s", recorder.Code, recorder.Body.String())
	}
}

func TestSitePreviews_ErrorStatuses(t *testing.T) {
	for err, want := range map[error]int{
		sites.ErrPreviewNotFound:            http.StatusNotFound,
		sites.ErrPreviewRemoving:            http.StatusConflict,
		sites.ErrSiteBusy:                   http.StatusConflict,
		sitebuilds.ErrNotConnected:          http.StatusNotFound,
		&sitebuilds.InputError{Reason: "x"}: http.StatusBadRequest,
	} {
		recorder := httptest.NewRecorder()
		writePreviewError(recorder, err)
		if recorder.Code != want {
			t.Errorf("%v = %d, want %d", err, recorder.Code, want)
		}
	}
}
