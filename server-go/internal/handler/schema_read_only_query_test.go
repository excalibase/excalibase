package handler

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/auth"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/go-chi/chi/v5"
)

// TestExecuteReadOnlyQueryRefusesBeforeOpeningTheDatabase pins the request
// checks of GET /query: no statement, or one too long for a query string, is
// refused without touching the project's database.
func TestExecuteReadOnlyQueryRefusesBeforeOpeningTheDatabase(t *testing.T) {
	h := schemaHandlerOver(t, nil)
	r := chi.NewRouter()
	r.Get("/api/schema/{projectId}/query", h.ExecuteReadOnlyQuery)
	cases := map[string]struct {
		target string
		want   int
	}{
		"no statement": {"/api/schema/proj-a/query", http.StatusBadRequest},
		"too long":     {"/api/schema/proj-a/query?sql=" + url.QueryEscape("SELECT '"+strings.Repeat("x", 17<<10)+"'"), http.StatusRequestURITooLong},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			w := httptest.NewRecorder()
			r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, tc.target, nil))
			if w.Code != tc.want {
				t.Fatalf("got %d %s, want %d", w.Code, w.Body.String(), tc.want)
			}
		})
	}
}

func TestExecuteReadOnlyQueryRefusesASignInSession(t *testing.T) {
	h := schemaHandlerOver(t, nil)
	r := chi.NewRouter()
	r.Get("/api/schema/{projectId}/query", h.ExecuteReadOnlyQuery)
	req := httptest.NewRequest(http.MethodGet, "/api/schema/proj-a/query?sql=select+1", nil)
	req = req.WithContext(auth.SetToken(req.Context(), &domain.AccessToken{Scopes: auth.ScopeSession}))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("got %d %s, want 403", w.Code, w.Body.String())
	}
}
