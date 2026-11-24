package middleware

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/orgguard"
	"github.com/excalibase/provisioning-poc/internal/testutil/fakestore"
	"github.com/go-chi/chi/v5"
)

func suspendedGuard(suspended bool) *orgguard.Guard {
	instances := fakestore.NewInstances()
	instances.Create(&domain.DatabaseInstance{ProjectID: "proj-s", OrgID: "org-s", Status: "ACTIVE"})
	return orgguard.New(instances, func(context.Context) (map[string]bool, error) {
		if suspended {
			return map[string]bool{"org-s": true}, nil
		}
		return map[string]bool{}, nil
	}).WithServingTTL(0)
}

func serveReq(handler http.Handler, method, path string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest(method, path, strings.NewReader("{}")))
	return w
}

func TestRefuseSuspendedWrites(t *testing.T) {
	ok := func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }
	build := func(guard *orgguard.Guard) http.Handler {
		r := chi.NewRouter()
		r.Route("/api/projects/{projectId}", func(r chi.Router) {
			r.Use(RefuseSuspendedWrites(guard))
			r.HandleFunc("/*", ok)
		})
		return r
	}
	cases := []struct {
		name, method, path string
		suspended          bool
		want               int
	}{
		{"write refused", http.MethodPost, "/api/projects/proj-s/apps/", true, http.StatusForbidden},
		{"put refused", http.MethodPut, "/api/projects/proj-s/db-endpoint/", true, http.StatusForbidden},
		{"read allowed", http.MethodGet, "/api/projects/proj-s/apps/", true, http.StatusNoContent},
		{"delete allowed", http.MethodDelete, "/api/projects/proj-s/apps/a1", true, http.StatusNoContent},
		{"pause allowed", http.MethodPost, "/api/projects/proj-s/pause", true, http.StatusNoContent},
		{"good standing", http.MethodPost, "/api/projects/proj-s/apps/", false, http.StatusNoContent},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := serveReq(build(suspendedGuard(tc.suspended)), tc.method, tc.path).Code; got != tc.want {
				t.Fatalf("got %d, want %d", got, tc.want)
			}
		})
	}
	if got := serveReq(build(nil), http.MethodPost, "/api/projects/proj-s/apps/").Code; got != http.StatusNoContent {
		t.Fatalf("nil guard: got %d, want it to allow", got)
	}
}

func TestRequireServableProjectRefusesASuspendedOwnersPublicFiles(t *testing.T) {
	instances := fakestore.NewInstances()
	instances.Create(&domain.DatabaseInstance{ProjectID: "proj-s", OrgID: "org-s", Status: "ACTIVE"})
	build := func(guard *orgguard.Guard) http.Handler {
		r := chi.NewRouter()
		r.With(RequireServableProject(instances, guard)).Get("/storage/v1/object/public/{projectId}/{bucket}/*",
			func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
		return r
	}
	path := "/storage/v1/object/public/proj-s/pub/logo.png"
	if got := serveReq(build(suspendedGuard(true)), http.MethodGet, path).Code; got != http.StatusNotFound {
		t.Fatalf("suspended: got %d, want 404", got)
	}
	if got := serveReq(build(suspendedGuard(false)), http.MethodGet, path).Code; got != http.StatusOK {
		t.Fatalf("good standing: got %d, want 200", got)
	}
}
