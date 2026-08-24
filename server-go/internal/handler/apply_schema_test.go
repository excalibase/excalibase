package handler

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/edgefn"
	"github.com/go-chi/chi/v5"
)

func applySchemaRouter(h *FunctionHandler) chi.Router {
	r := chi.NewRouter()
	r.Post("/api/projects/{projectId}/schema/apply", h.ApplySchemaFromStore)
	return r
}

func TestApplySchemaFromStore_InvalidProjectID(t *testing.T) {
	store := edgefn.NewFunctionStore(t.TempDir())
	h := NewFunctionHandler(store, nil, nil, nil, nil, "")
	r := applySchemaRouter(h)

	req := httptest.NewRequest("POST", "/api/projects/..%2Fevil/schema/apply", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("invalid project id should 400, got %d", w.Code)
	}
}

func TestApplySchemaFromStore_NotConfigured(t *testing.T) {
	store := edgefn.NewFunctionStore(t.TempDir())
	h := NewFunctionHandler(store, nil, nil, nil, nil, "")
	// projectDBFn unset → 503.
	r := applySchemaRouter(h)
	req := httptest.NewRequest("POST", "/api/projects/proj_a/schema/apply", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("missing projectDBFn should 503, got %d", w.Code)
	}
}

func TestApplySchemaFromStore_NoSchemasAppliesZero(t *testing.T) {
	store := edgefn.NewFunctionStore(t.TempDir())
	// A function with no SchemaJSON → nothing to apply.
	if err := store.Save(&edgefn.Function{
		ID: "fn1", ProjectID: "proj_a", Name: "fn1",
		Files: []edgefn.File{{Path: "index.ts", Content: "export default () => new Response('ok')"}},
	}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	h := NewFunctionHandler(store, nil, nil, nil, nil, "")
	// DB resolver returns a nil DB; since no function carries SchemaJSON,
	// ApplySchema is never invoked, so the nil DB is never dereferenced.
	h.SetProjectDBFn(func(_ context.Context, _ string) (*sql.DB, error) {
		return nil, nil
	})
	r := applySchemaRouter(h)
	req := httptest.NewRequest("POST", "/api/projects/proj_a/schema/apply", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", w.Code, w.Body.String())
	}
	var resp map[string]any
	json.Unmarshal(w.Body.Bytes(), &resp)
	if resp["status"] != "applied" {
		t.Errorf("status: got %v", resp["status"])
	}
}
