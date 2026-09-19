package handler

import (
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/auth"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/storage"
	"github.com/go-chi/chi/v5"
)

// paramGroupRouter mounts the parameter-group routes the way main.go does —
// behind RequireAuth — with the caller's platform role under test.
func paramGroupRouter(t *testing.T, role string) (chi.Router, string) {
	t.Helper()
	dir := t.TempDir()
	store, err := storage.NewFileSystemParameterGroupStore(dir)
	if err != nil {
		t.Fatalf("NewFileSystemParameterGroupStore: %v", err)
	}
	handler := NewParameterGroupHandler(store)

	router := chi.NewRouter()
	router.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			user := &domain.User{ID: "caller", Role: role, Active: true}
			next.ServeHTTP(w, req.WithContext(auth.SetUser(req.Context(), user)))
		})
	})
	router.Route("/api/parameter-groups", func(r chi.Router) {
		r.Use(auth.RequireAuth)
		handler.Routes(r)
	})
	return router, dir
}

// TestParameterGroupTraversalNameRejected reproduces EXC-398 finding 4: a
// name carrying a traversal must be refused at the handler boundary and must
// write nothing outside the parameter-group directory.
func TestParameterGroupTraversalNameRejected(t *testing.T) {
	router, dir := paramGroupRouter(t, "platform_admin")
	victim := filepath.Join(dir, "snapshots")
	if err := os.MkdirAll(victim, 0755); err != nil {
		t.Fatalf("seed victim dir: %v", err)
	}
	original := []byte(`{"id":"proj-b-20260919-120000","projectId":"proj-b"}`)
	victimFile := filepath.Join(victim, "proj-b-20260919-120000.json")
	if err := os.WriteFile(victimFile, original, 0644); err != nil {
		t.Fatalf("seed victim file: %v", err)
	}

	body := `{"name":"../snapshots/proj-b-20260919-120000","parameters":{}}`
	w := doRequest(router, "POST", "/api/parameter-groups/", body)
	if w.Code != http.StatusBadRequest {
		t.Errorf("traversal create: got %d, want 400 (body: %s)", w.Code, w.Body.String())
	}

	after, err := os.ReadFile(victimFile)
	if err != nil {
		t.Fatalf("victim file gone: %v", err)
	}
	if string(after) != string(original) {
		t.Errorf("victim file overwritten:\n%s", after)
	}
}

// TestParameterGroupNameValidatedOnEverySurface covers the whole handler:
// every route that takes a name rejects a malformed one.
func TestParameterGroupNameValidatedOnEverySurface(t *testing.T) {
	router, _ := paramGroupRouter(t, "platform_admin")
	cases := []struct {
		name, method, path, body string
	}{
		{"create empty name", "POST", "/api/parameter-groups/", `{"name":"","parameters":{}}`},
		{"create traversal", "POST", "/api/parameter-groups/", `{"name":"..","parameters":{}}`},
		{"create separator", "POST", "/api/parameter-groups/", `{"name":"a/b","parameters":{}}`},
		{"get traversal", "GET", "/api/parameter-groups/..%2f..%2fusers", ""},
		{"update traversal", "PUT", "/api/parameter-groups/..%2f..%2fusers", `{"parameters":{}}`},
		{"delete traversal", "DELETE", "/api/parameter-groups/..%2f..%2fusers", ""},
		{"delete leading dot", "DELETE", "/api/parameter-groups/.hidden", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w := doRequest(router, c.method, c.path, c.body)
			if w.Code != http.StatusBadRequest {
				t.Errorf("%s %s: got %d, want 400", c.method, c.path, w.Code)
			}
		})
	}
}

// TestParameterGroupDecodeErrorIsBadRequest pins that a malformed body is a
// 400, not a silently accepted empty group.
func TestParameterGroupDecodeErrorIsBadRequest(t *testing.T) {
	router, _ := paramGroupRouter(t, "platform_admin")
	for _, path := range []string{"/api/parameter-groups/", "/api/parameter-groups/valid-name"} {
		method := "POST"
		if path != "/api/parameter-groups/" {
			method = "PUT"
		}
		w := doRequest(router, method, path, `{"name":`)
		if w.Code != http.StatusBadRequest {
			t.Errorf("%s %s with malformed JSON: got %d, want 400", method, path, w.Code)
		}
	}
}

// TestParameterGroupWritesRequirePlatformAdmin reproduces the authorization
// half of EXC-398 finding 4: these groups are global, so an ordinary user
// must not create, update or delete one. Reading stays open to any
// authenticated caller (the provision page's selector).
func TestParameterGroupWritesRequirePlatformAdmin(t *testing.T) {
	router, _ := paramGroupRouter(t, "user")
	writes := []struct {
		method, path, body string
	}{
		{"POST", "/api/parameter-groups/", `{"name":"high-perf","parameters":{}}`},
		{"PUT", "/api/parameter-groups/high-perf", `{"parameters":{}}`},
		{"DELETE", "/api/parameter-groups/high-perf", ""},
	}
	for _, c := range writes {
		t.Run(c.method, func(t *testing.T) {
			w := doRequest(router, c.method, c.path, c.body)
			if w.Code != http.StatusForbidden {
				t.Errorf("%s %s as ordinary user: got %d, want 403", c.method, c.path, w.Code)
			}
		})
	}

	if w := doRequest(router, "GET", "/api/parameter-groups/", ""); w.Code != http.StatusOK {
		t.Errorf("list as ordinary user: got %d, want 200", w.Code)
	}
}

// TestParameterGroupAdminRoundTrip keeps the legitimate path working.
func TestParameterGroupAdminRoundTrip(t *testing.T) {
	router, _ := paramGroupRouter(t, "platform_admin")

	if w := doRequest(router, "POST", "/api/parameter-groups/", `{"name":"high-perf","parameters":{"max_connections":"200"}}`); w.Code != http.StatusCreated {
		t.Fatalf("create: got %d, body: %s", w.Code, w.Body.String())
	}
	if w := doRequest(router, "GET", "/api/parameter-groups/high-perf", ""); w.Code != http.StatusOK {
		t.Errorf("get: got %d", w.Code)
	}
	if w := doRequest(router, "PUT", "/api/parameter-groups/high-perf", `{"parameters":{"max_connections":"400"}}`); w.Code != http.StatusOK {
		t.Errorf("update: got %d", w.Code)
	}
	if w := doRequest(router, "DELETE", "/api/parameter-groups/high-perf", ""); w.Code != http.StatusOK {
		t.Errorf("delete: got %d", w.Code)
	}
}
