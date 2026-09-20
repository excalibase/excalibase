package handler

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/auth"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/go-chi/chi/v5"
)

const testLocalhost = "127.0.0.1"

// setupRealtimeRouter wires a RealtimeHandler with an in-memory instance
// store and a fake vault. Routes are mounted under /api/projects/{projectId}/realtime
// to match the production wiring; the handler reads `projectId` from the URL.
func setupRealtimeRouter(t *testing.T, vaultCreds map[string]string) (chi.Router, *inMemoryInstanceStore, *fakeVault) {
	t.Helper()
	v := newFakeVault()
	if vaultCreds != nil {
		v.Put("projects/proj-known/credentials/excalibase_app", vaultCreds)
	}
	insts := &inMemoryInstanceStore{insts: map[string]*domain.DatabaseInstance{
		"proj-known": {ProjectID: "proj-known", OrgID: "default"},
	}}
	h := NewRealtimeHandler(insts, nil, v)
	h.SetPublicationName("custom_pub")

	r := chi.NewRouter()
	// Inject fake user so RequireAuth doesn't block — same pattern as vault tests.
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			user := &domain.User{ID: "u1", Username: "u", Role: "platform_admin", Active: true}
			next.ServeHTTP(w, r.WithContext(auth.SetUser(r.Context(), user)))
		})
	})
	r.Route("/api/projects/{projectId}/realtime", func(r chi.Router) { h.Routes(r) })
	return r, insts, v
}

// Each endpoint hit verifies one branch — invalid project, missing project,
// missing creds, or DB-connect failure (with a junk DSN). All resolve to
// 500-class responses, but they exercise the dial() pipeline + the per-method
// wrapper code that callers care about for coverage.

func TestRealtimeHandler_ListTables_InvalidProjectID(t *testing.T) {
	r, _, _ := setupRealtimeRouter(t, nil)
	req := httptest.NewRequest("GET", "/api/projects/bad..id/realtime/tables", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != 500 {
		t.Errorf("invalid id: got %d, want 500", w.Code)
	}
}

func TestRealtimeHandler_ListTables_VaultMissingCreds(t *testing.T) {
	r, _, _ := setupRealtimeRouter(t, nil) // no creds seeded
	req := httptest.NewRequest("GET", "/api/projects/proj-known/realtime/tables", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != 500 {
		t.Errorf("missing creds: got %d, want 500", w.Code)
	}
}

func TestRealtimeHandler_ListTables_DialFailure_DBUnreachable(t *testing.T) {
	r, _, _ := setupRealtimeRouter(t, map[string]string{
		"username": "u", "password": "p",
		"host": testLocalhost, "port": "1", "database": "x",
	})
	req := httptest.NewRequest("GET", "/api/projects/proj-known/realtime/tables", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	// DB connection refused on port 1 → ListTables returns error → 500.
	if w.Code != 500 {
		t.Errorf("dial failure: got %d, want 500", w.Code)
	}
}

func TestRealtimeHandler_EnableTable_DialFailure(t *testing.T) {
	r, _, _ := setupRealtimeRouter(t, map[string]string{
		"username": "u", "password": "p",
		"host": testLocalhost, "port": "1", "database": "x",
	})
	req := httptest.NewRequest("PUT", "/api/projects/proj-known/realtime/tables/public/posts", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != 400 && w.Code != 500 {
		t.Errorf("EnableTable dial-fail: got %d, want 400 or 500", w.Code)
	}
}

func TestRealtimeHandler_DisableTable_DialFailure(t *testing.T) {
	r, _, _ := setupRealtimeRouter(t, map[string]string{
		"username": "u", "password": "p",
		"host": testLocalhost, "port": "1", "database": "x",
	})
	req := httptest.NewRequest("DELETE", "/api/projects/proj-known/realtime/tables/public/posts", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != 400 && w.Code != 500 {
		t.Errorf("DisableTable dial-fail: got %d, want 400 or 500", w.Code)
	}
}

func TestRealtimeHandler_EnableAll_DialFailure(t *testing.T) {
	r, _, _ := setupRealtimeRouter(t, map[string]string{
		"username": "u", "password": "p",
		"host": testLocalhost, "port": "1", "database": "x",
	})
	req := httptest.NewRequest("POST", "/api/projects/proj-known/realtime/enable-all", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != 500 {
		t.Errorf("EnableAll: got %d, want 500", w.Code)
	}
}

func TestRealtimeHandler_DisableAll_DialFailure(t *testing.T) {
	r, _, _ := setupRealtimeRouter(t, map[string]string{
		"username": "u", "password": "p",
		"host": testLocalhost, "port": "1", "database": "x",
	})
	req := httptest.NewRequest("POST", "/api/projects/proj-known/realtime/disable-all", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != 500 {
		t.Errorf("DisableAll: got %d, want 500", w.Code)
	}
}

func TestRealtimeHandler_NewAndSetPublicationName(t *testing.T) {
	v := newFakeVault()
	insts := &inMemoryInstanceStore{insts: map[string]*domain.DatabaseInstance{}}
	h := NewRealtimeHandler(insts, nil, v)
	if h.publicationName != "" {
		t.Error("publicationName should default to empty (service applies fallback)")
	}
	h.SetPublicationName("xyz")
	if h.publicationName != "xyz" {
		t.Errorf("publicationName: got %q, want xyz", h.publicationName)
	}
}
