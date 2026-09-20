package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/testutil/fakestore"
	"github.com/go-chi/chi/v5"
)

// The public object route is anonymous and sits outside the project-access
// gate, so it carries the rule itself: a project the platform must not serve
// must not have downloads signed for it.
func TestRequireServableProjectGuardsAnAnonymousRoute(t *testing.T) {
	cases := map[string]struct {
		status string
		want   int
	}{
		"active":    {"ACTIVE", http.StatusOK},
		"paused":    {string(domain.StatusPaused), http.StatusOK},
		"restoring": {string(domain.StatusRestoring), http.StatusNotFound},
		"deleting":  {string(domain.StatusDeleting), http.StatusNotFound},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			instances := fakestore.NewInstances()
			instances.Create(&domain.DatabaseInstance{ProjectID: "proj-s", OrgID: "o", Status: tc.status})

			r := chi.NewRouter()
			r.With(RequireServableProject(instances)).Get("/storage/v1/object/public/{projectId}/{bucket}/*",
				func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })

			req := httptest.NewRequest(http.MethodGet, "/storage/v1/object/public/proj-s/pub/logo.png", nil)
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)
			if w.Code != tc.want {
				t.Errorf("got %d, want %d", w.Code, tc.want)
			}
		})
	}
}

func TestRequireServableProjectAnswers404ForAnUnknownProject(t *testing.T) {
	r := chi.NewRouter()
	r.With(RequireServableProject(fakestore.NewInstances())).Get("/storage/v1/object/public/{projectId}/{bucket}/*",
		func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })

	req := httptest.NewRequest(http.MethodGet, "/storage/v1/object/public/nope/pub/x.png", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Errorf("got %d, want 404", w.Code)
	}
}

// Without a store the middleware is a pass-through rather than a closed door:
// a deployment that never wired it must not lose its public downloads.
func TestRequireServableProjectWithoutAStoreIsAPassThrough(t *testing.T) {
	r := chi.NewRouter()
	r.With(RequireServableProject(nil)).Get("/storage/v1/object/public/{projectId}/{bucket}/*",
		func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })

	req := httptest.NewRequest(http.MethodGet, "/storage/v1/object/public/proj-s/pub/x.png", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("got %d, want 200", w.Code)
	}
}
