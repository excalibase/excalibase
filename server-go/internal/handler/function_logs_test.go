package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/edgefn"
	"github.com/excalibase/provisioning-poc/internal/storage"
	"github.com/go-chi/chi/v5"
)

// setupFnHandlerWithLogs is a thinner version of setupFunctionHandler that
// also wires the Logs and RuntimeStatus routes. Those endpoints aren't part
// of the smoke setup but are exercised by Studio's logs panel + status badge.
func setupFnHandlerWithLogs(t *testing.T, runtimeURL string) (chi.Router, *edgefn.FunctionStore) {
	t.Helper()
	dir := t.TempDir()
	store := edgefn.NewFunctionStore(dir)

	v := newFakeVault()
	secrets := edgefn.NewSecretsStore(v)
	client := edgefn.NewRuntimeClient(runtimeURL, "")

	instStore := &inMemoryInstanceStore{insts: map[string]*domain.DatabaseInstance{
		"proj_p1": {ProjectID: "proj_p1", OrgID: "default"},
	}}
	var orgStore storage.OrgStore
	h := NewFunctionHandler(store, secrets, client, instStore, orgStore, "https://api.test.io")

	r := chi.NewRouter()
	r.Route("/api/projects/{projectId}/functions", func(r chi.Router) {
		r.Get("/runtime/status", h.RuntimeStatus)
		r.Post("/", h.Create)
		r.Route("/{fnId}", func(r chi.Router) {
			r.Get("/logs", h.Logs)
		})
	})
	return r, store
}

func TestFunctionHandler_Logs_FunctionNotFound(t *testing.T) {
	srv, _ := mockFnRuntime(t)
	r, _ := setupFnHandlerWithLogs(t, srv.URL)

	req := httptest.NewRequest("GET", "/api/projects/proj_p1/functions/missing/logs", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Errorf("missing fn: got %d, want 404", w.Code)
	}
}

func TestFunctionHandler_Logs_HappyPath(t *testing.T) {
	srv, _ := mockFnRuntime(t)
	r, store := setupFnHandlerWithLogs(t, srv.URL)

	// Seed a function so /logs/{fnId} resolves to a real RuntimeID.
	fn := &edgefn.Function{
		ID: "hello", Name: "Hello", ProjectID: "proj_p1",
		Files: []edgefn.File{{Path: "index.ts", Content: "export default () => new Response('ok')"}},
	}
	if err := store.Save(fn); err != nil {
		t.Fatalf("seed function: %v", err)
	}

	req := httptest.NewRequest("GET", "/api/projects/proj_p1/functions/hello/logs?since=0", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	// The mock runtime returns 404 for /logs/* — handler treats that as
	// ErrLogsNotFound and returns an empty list with 200.
	if w.Code != http.StatusOK {
		t.Errorf("logs: got %d body=%s", w.Code, w.Body.String())
	}
	var body struct {
		Logs []edgefn.LogEntry `json:"logs"`
	}
	json.NewDecoder(w.Body).Decode(&body)
	if body.Logs == nil {
		t.Error("logs slice should be non-nil (handler normalises null → [])")
	}
}

func TestFunctionHandler_Logs_SinceFilter(t *testing.T) {
	srv, _ := mockFnRuntime(t)
	r, store := setupFnHandlerWithLogs(t, srv.URL)

	fn := &edgefn.Function{
		ID: "hello2", Name: "Hello", ProjectID: "proj_p1",
		Files: []edgefn.File{{Path: "index.ts", Content: "x"}},
	}
	store.Save(fn)

	// Non-numeric since is silently ignored — exercises the parse-fallback branch.
	req := httptest.NewRequest("GET", "/api/projects/proj_p1/functions/hello2/logs?since=abc", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("bad since: got %d, want 200 (handler should swallow parse errors)", w.Code)
	}
}

// RuntimeStatus

func TestFunctionHandler_RuntimeStatus_Healthy(t *testing.T) {
	srv, _ := mockFnRuntime(t)
	r, _ := setupFnHandlerWithLogs(t, srv.URL)

	req := httptest.NewRequest("GET", "/api/projects/proj_p1/functions/runtime/status", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("runtime status: got %d", w.Code)
	}
	var body map[string]interface{}
	json.NewDecoder(w.Body).Decode(&body)
	if body["status"] != "healthy" {
		t.Errorf("expected healthy, got %v", body["status"])
	}
	if body["healthy"] != true {
		t.Errorf("expected healthy=true, got %v", body["healthy"])
	}
}

func TestFunctionHandler_RuntimeStatus_Unhealthy_NoBackend(t *testing.T) {
	// Point at a closed port so Health returns an error.
	r, _ := setupFnHandlerWithLogs(t, "http://127.0.0.1:1")

	req := httptest.NewRequest("GET", "/api/projects/proj_p1/functions/runtime/status", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("status code: got %d, want 200 (handler always 200, status field carries truth)", w.Code)
	}
	var body map[string]interface{}
	json.NewDecoder(w.Body).Decode(&body)
	if body["status"] != "unavailable" {
		t.Errorf("expected unavailable, got %v", body["status"])
	}
}
