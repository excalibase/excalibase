package middleware

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/testutil/fakestore"
	"github.com/go-chi/chi/v5"
)

func TestRequireProjectDatabaseRefusesAProjectWithoutOne(t *testing.T) {
	cases := map[string]struct {
		project *domain.DatabaseInstance
		want    int
		body    string
	}{
		"with a database":    {&domain.DatabaseInstance{ProjectID: "proj-d", OrgID: "o", Status: "ACTIVE"}, http.StatusOK, ""},
		"without a database": {&domain.DatabaseInstance{ProjectID: "proj-d", OrgID: "o", Status: "ACTIVE", NoDatabase: true}, http.StatusConflict, "project has no database"},
		// An unknown project is the access gate's answer to give, not this one's.
		"unknown project": {nil, http.StatusNotFound, "project not found"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			instances := fakestore.NewInstances()
			if tc.project != nil {
				instances.Create(tc.project)
			}
			r := chi.NewRouter()
			r.With(RequireProjectDatabase(instances)).Get("/api/projects/{projectId}/realtime/tables",
				func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })

			w := httptest.NewRecorder()
			r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/projects/proj-d/realtime/tables", nil))
			if w.Code != tc.want {
				t.Fatalf("got %d, want %d", w.Code, tc.want)
			}
			if !strings.Contains(w.Body.String(), tc.body) {
				t.Fatalf("body %q lacks %q", w.Body.String(), tc.body)
			}
		})
	}
}

// A store that cannot answer is not read as "has a database".
func TestRequireProjectDatabaseFailsClosedWithoutAnAnswer(t *testing.T) {
	r := chi.NewRouter()
	r.With(RequireProjectDatabase(nil)).Get("/api/projects/{projectId}/x",
		func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/projects/proj-d/x", nil))
	if w.Code == http.StatusOK {
		t.Fatal("a request passed with no store to ask")
	}
}

func TestRequireProjectDatabaseAnswers503WhenTheStoreCannotSay(t *testing.T) {
	instances := fakestore.NewInstances()
	instances.Err = errors.New("platform db down")
	r := chi.NewRouter()
	r.With(RequireProjectDatabase(instances)).Get("/api/projects/{projectId}/x",
		func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/projects/proj-d/x", nil))
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("got %d, want 503", w.Code)
	}
}

// EXC-619: a refused MySQL route says why, so Studio and MCP can show it.
func TestRequireProjectDatabaseNamesWhyAMySQLProjectIsRefused(t *testing.T) {
	instances := fakestore.NewInstances()
	instances.Create(&domain.DatabaseInstance{ProjectID: "proj-m", OrgID: "o", Status: "ACTIVE", DBType: domain.MySQL})
	r := chi.NewRouter()
	r.With(RequireProjectDatabase(instances)).Get("/api/schema/{projectId}/extensions",
		func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	r.With(RequireProjectDatabase(instances)).Get("/api/schema/{projectId}/tables",
		func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })

	refused := httptest.NewRecorder()
	r.ServeHTTP(refused, httptest.NewRequest(http.MethodGet, "/api/schema/proj-m/extensions", nil))
	if refused.Code != http.StatusConflict || !strings.Contains(refused.Body.String(), "not available for MySQL projects: MySQL has no extensions") {
		t.Fatalf("extensions: %d %s", refused.Code, refused.Body.String())
	}
	served := httptest.NewRecorder()
	r.ServeHTTP(served, httptest.NewRequest(http.MethodGet, "/api/schema/proj-m/tables", nil))
	if served.Code != http.StatusOK {
		t.Fatalf("tables: %d %s", served.Code, served.Body.String())
	}
}
