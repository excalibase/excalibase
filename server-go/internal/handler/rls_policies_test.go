//go:build integration

package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/domain"
	pgtest "github.com/excalibase/provisioning-poc/internal/testutil/pgstore"
	"github.com/go-chi/chi/v5"
)

// captureBus records every PublishPolicyChange call so tests can assert on
// what would have hit NATS without dialing.
type captureBus struct {
	mu     sync.Mutex
	events []domain.PolicyChangeEvent
}

func (c *captureBus) PublishPolicyChange(_ context.Context, e domain.PolicyChangeEvent) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.events = append(c.events, e)
}

func (c *captureBus) snapshot() []domain.PolicyChangeEvent {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]domain.PolicyChangeEvent, len(c.events))
	copy(out, c.events)
	return out
}

// setupRlsRouter wires a Postgres store + handler under the same
// path shape that mountProvisioningRoutes uses, so /{projectId} chi param is
// real. Returns the router + the captured event bus so tests can assert
// on both HTTP response and emitted events.
func setupRlsRouter(t *testing.T) (chi.Router, *captureBus) {
	t.Helper()
	store := pgtest.New(t)

	bus := &captureBus{}
	h := NewRlsPolicyHandler(store.RlsPolicies())
	h.SetPublisher(bus)

	r := chi.NewRouter()
	r.Route("/api/provision/{projectId}/rls-policies", func(r chi.Router) { h.RlsRoutes(r) })
	r.Route("/api/provision/{projectId}/column-policies", func(r chi.Router) { h.ColumnRoutes(r) })
	return r, bus
}

// jsonBody marshals v and returns a *bytes.Reader suitable for httptest.NewRequest.
func rlsJSONBody(t *testing.T, v any) *bytes.Reader {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return bytes.NewReader(b)
}

func validPolicy() map[string]any {
	return map[string]any{
		"name":       "owner-can-read",
		"resource":   "orders",
		"effect":     "ALLOW",
		"operations": []string{"SELECT"},
		"ruleLogic":  "AND",
		"rules": []map[string]any{
			{"field": "user_id", "fieldType": "STRING", "operator": "EQ", "value": "ctx.user_id"},
		},
		"assignments": []map[string]any{
			{"targetType": "USER", "targetId": "*"},
		},
		"priority": 100,
		"enabled":  true,
	}
}

func TestRlsHandler_Create_ReturnsCreatedAndEmitsEvent(t *testing.T) {
	r, bus := setupRlsRouter(t)
	req := httptest.NewRequest("POST", "/api/provision/proj-a/rls-policies/", rlsJSONBody(t, validPolicy()))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("status: got %d, body=%s", w.Code, w.Body.String())
	}

	var resp domain.Policy
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.ProjectID != "proj-a" {
		t.Errorf("response projectId not set from path: %s", resp.ProjectID)
	}
	if resp.ID == "" {
		t.Error("response should include server-assigned id")
	}

	events := bus.snapshot()
	if len(events) != 1 || events[0].Op != "create" || events[0].Kind != "rls" || events[0].Resource != "orders" {
		t.Errorf("expected one create event for orders, got %+v", events)
	}
}

func TestRlsHandler_BodyProjectIDIgnored(t *testing.T) {
	// Even if the client sends projectId in the body, the path's projectId wins.
	r, _ := setupRlsRouter(t)
	body := validPolicy()
	body["projectId"] = "attacker-controlled"
	req := httptest.NewRequest("POST", "/api/provision/proj-a/rls-policies/", rlsJSONBody(t, body))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("status: %d body=%s", w.Code, w.Body.String())
	}
	var resp domain.Policy
	json.Unmarshal(w.Body.Bytes(), &resp)
	if resp.ProjectID != "proj-a" {
		t.Errorf("body projectId leaked through: got %q", resp.ProjectID)
	}
}

func TestRlsHandler_Create_RejectsInvalidPayload(t *testing.T) {
	cases := []struct {
		name       string
		mutate     func(p map[string]any)
		wantSubstr string
	}{
		{"empty name", func(p map[string]any) { p["name"] = "" }, "name required"},
		{"bad resource", func(p map[string]any) { p["resource"] = "drop table users;--" }, "invalid resource"},
		{"bad effect", func(p map[string]any) { p["effect"] = "MAYBE" }, "effect must be"},
		{"no operations", func(p map[string]any) { p["operations"] = []string{} }, "at least one operation"},
		{"unknown operation", func(p map[string]any) { p["operations"] = []string{"DROP"} }, "unknown operation"},
		{"no rules", func(p map[string]any) { p["rules"] = []any{} }, "at least one rule"},
		{"no assignments", func(p map[string]any) { p["assignments"] = []any{} }, "at least one assignment"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r, _ := setupRlsRouter(t)
			body := validPolicy()
			tc.mutate(body)
			req := httptest.NewRequest("POST", "/api/provision/proj-a/rls-policies/", rlsJSONBody(t, body))
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)
			if w.Code != http.StatusBadRequest {
				t.Fatalf("want 400, got %d body=%s", w.Code, w.Body.String())
			}
			if !strings.Contains(w.Body.String(), tc.wantSubstr) {
				t.Errorf("body missing %q: %s", tc.wantSubstr, w.Body.String())
			}
		})
	}
}

func TestRlsHandler_Get_NotFound(t *testing.T) {
	r, _ := setupRlsRouter(t)
	req := httptest.NewRequest("GET", "/api/provision/proj-a/rls-policies/missing", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Errorf("got %d, want 404", w.Code)
	}
}

func TestRlsHandler_List_ScopedByProject(t *testing.T) {
	r, _ := setupRlsRouter(t)

	// Create one in proj-a, one in proj-b.
	createPolicy(t, r, "proj-a", validPolicy())
	createPolicy(t, r, "proj-b", validPolicy())

	req := httptest.NewRequest("GET", "/api/provision/proj-a/rls-policies/", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	var out []domain.Policy
	json.Unmarshal(w.Body.Bytes(), &out)
	if len(out) != 1 || out[0].ProjectID != "proj-a" {
		t.Errorf("list scope: %+v", out)
	}
}

func TestRlsHandler_Delete_Emits(t *testing.T) {
	r, bus := setupRlsRouter(t)
	created := createPolicy(t, r, "proj-a", validPolicy())

	req := httptest.NewRequest("DELETE", "/api/provision/proj-a/rls-policies/"+created.ID, nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusNoContent {
		t.Errorf("delete: got %d", w.Code)
	}
	events := bus.snapshot()
	if len(events) < 2 || events[len(events)-1].Op != "delete" {
		t.Errorf("expected trailing delete event, got %+v", events)
	}
}

func TestRlsHandler_Delete_NotFoundReturns404(t *testing.T) {
	r, bus := setupRlsRouter(t)
	req := httptest.NewRequest("DELETE", "/api/provision/proj-a/rls-policies/never-existed", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Errorf("got %d, want 404", w.Code)
	}
	if len(bus.snapshot()) != 0 {
		t.Errorf("404 path should not emit events")
	}
}

func TestRlsHandler_InvalidProjectID(t *testing.T) {
	r, _ := setupRlsRouter(t)
	// Path-traversal-ish project id must be rejected before we touch storage.
	req := httptest.NewRequest("POST", "/api/provision/..%2Fevil/rls-policies/", rlsJSONBody(t, validPolicy()))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("got %d body=%s, want 400", w.Code, w.Body.String())
	}
}

// -------------------- Column policy handler tests --------------------

func validColumnPolicy() map[string]any {
	return map[string]any{
		"name":       "mask-pii",
		"resource":   "users",
		"columns":    []string{"email", "phone"},
		"operations": []string{"SELECT"},
		"mode":       "NULL",
		"assignments": []map[string]any{
			{"targetType": "ROLE", "targetId": "viewer"},
		},
		"priority": 10,
		"enabled":  true,
	}
}

func TestColumnHandler_Create_PartialRequiresSpec(t *testing.T) {
	r, _ := setupRlsRouter(t)
	body := validColumnPolicy()
	body["mode"] = "PARTIAL"
	req := httptest.NewRequest("POST", "/api/provision/proj-a/column-policies/", rlsJSONBody(t, body))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("got %d, want 400 — PARTIAL without spec", w.Code)
	}
	if !strings.Contains(w.Body.String(), "PARTIAL mode requires") {
		t.Errorf("error message missing 'PARTIAL mode requires': %s", w.Body.String())
	}
}

func TestColumnHandler_Create_CustomRequiresKey(t *testing.T) {
	r, _ := setupRlsRouter(t)
	body := validColumnPolicy()
	body["mode"] = "CUSTOM"
	req := httptest.NewRequest("POST", "/api/provision/proj-a/column-policies/", rlsJSONBody(t, body))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("got %d, want 400 — CUSTOM without key", w.Code)
	}
	if !strings.Contains(w.Body.String(), "CUSTOM mode requires") {
		t.Errorf("error missing 'CUSTOM mode requires': %s", w.Body.String())
	}
}

func TestColumnHandler_Create_HideMode_RejectsLeakedFields(t *testing.T) {
	r, _ := setupRlsRouter(t)
	body := validColumnPolicy()
	body["mode"] = "HIDE"
	body["customMaskerKey"] = "leak"
	req := httptest.NewRequest("POST", "/api/provision/proj-a/column-policies/", rlsJSONBody(t, body))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("HIDE with custom key should 400, got %d", w.Code)
	}
}

func TestColumnHandler_Create_RejectsInvalidColumns(t *testing.T) {
	r, _ := setupRlsRouter(t)
	body := validColumnPolicy()
	body["columns"] = []string{"email", "1; DROP TABLE users"}
	req := httptest.NewRequest("POST", "/api/provision/proj-a/column-policies/", rlsJSONBody(t, body))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("bad column name should 400, got %d body=%s", w.Code, w.Body.String())
	}
}

func TestColumnHandler_CreateAndDelete_EmitsEvents(t *testing.T) {
	r, bus := setupRlsRouter(t)

	req := httptest.NewRequest("POST", "/api/provision/proj-a/column-policies/", rlsJSONBody(t, validColumnPolicy()))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("create: got %d body=%s", w.Code, w.Body.String())
	}
	var created domain.ColumnPolicy
	json.Unmarshal(w.Body.Bytes(), &created)

	req = httptest.NewRequest("DELETE", "/api/provision/proj-a/column-policies/"+created.ID, nil)
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusNoContent {
		t.Fatalf("delete: got %d", w.Code)
	}

	events := bus.snapshot()
	if len(events) != 2 || events[0].Op != "create" || events[1].Op != "delete" {
		t.Errorf("expected create then delete, got %+v", events)
	}
	for _, e := range events {
		if e.Kind != "column" {
			t.Errorf("expected kind=column, got %q", e.Kind)
		}
	}
}

func TestRlsHandler_Update_RoundTrips(t *testing.T) {
	r, bus := setupRlsRouter(t)
	created := createPolicy(t, r, "proj-a", validPolicy())

	// PATCH a field and confirm it persists + emits an update event.
	patch := validPolicy()
	patch["priority"] = 250
	req := httptest.NewRequest("PATCH", "/api/provision/proj-a/rls-policies/"+created.ID, rlsJSONBody(t, patch))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("update: got %d body=%s", w.Code, w.Body.String())
	}
	var updated domain.Policy
	json.Unmarshal(w.Body.Bytes(), &updated)
	if updated.ID != created.ID {
		t.Errorf("update changed id: %s vs %s", updated.ID, created.ID)
	}
	events := bus.snapshot()
	if events[len(events)-1].Op != "update" {
		t.Errorf("expected trailing update event, got %+v", events)
	}
}

func TestRlsHandler_Update_NotFound(t *testing.T) {
	r, _ := setupRlsRouter(t)
	req := httptest.NewRequest("PATCH", "/api/provision/proj-a/rls-policies/ghost", rlsJSONBody(t, validPolicy()))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Errorf("update missing: got %d, want 404", w.Code)
	}
}

func TestColumnHandler_GetAndList(t *testing.T) {
	r, _ := setupRlsRouter(t)
	// Create a column policy.
	req := httptest.NewRequest("POST", "/api/provision/proj-a/column-policies/", rlsJSONBody(t, validColumnPolicy()))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("create: got %d body=%s", w.Code, w.Body.String())
	}
	var created domain.ColumnPolicy
	json.Unmarshal(w.Body.Bytes(), &created)

	// GET by id.
	req = httptest.NewRequest("GET", "/api/provision/proj-a/column-policies/"+created.ID, nil)
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("get: got %d", w.Code)
	}
	var got domain.ColumnPolicy
	json.Unmarshal(w.Body.Bytes(), &got)
	if got.ID != created.ID {
		t.Errorf("get id mismatch: %s vs %s", got.ID, created.ID)
	}

	// LIST scoped to project.
	req = httptest.NewRequest("GET", "/api/provision/proj-a/column-policies/", nil)
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("list: got %d", w.Code)
	}
	var list []domain.ColumnPolicy
	json.Unmarshal(w.Body.Bytes(), &list)
	if len(list) != 1 {
		t.Errorf("list: got %d, want 1", len(list))
	}
}

func TestColumnHandler_Get_NotFound(t *testing.T) {
	r, _ := setupRlsRouter(t)
	req := httptest.NewRequest("GET", "/api/provision/proj-a/column-policies/missing", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Errorf("get missing: got %d, want 404", w.Code)
	}
}

func TestColumnHandler_Update_RoundTrips(t *testing.T) {
	r, bus := setupRlsRouter(t)
	req := httptest.NewRequest("POST", "/api/provision/proj-a/column-policies/", rlsJSONBody(t, validColumnPolicy()))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("create: got %d body=%s", w.Code, w.Body.String())
	}
	var created domain.ColumnPolicy
	json.Unmarshal(w.Body.Bytes(), &created)

	patch := validColumnPolicy()
	patch["priority"] = 99
	req = httptest.NewRequest("PATCH", "/api/provision/proj-a/column-policies/"+created.ID, rlsJSONBody(t, patch))
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("update: got %d body=%s", w.Code, w.Body.String())
	}
	events := bus.snapshot()
	if events[len(events)-1].Op != "update" || events[len(events)-1].Kind != "column" {
		t.Errorf("expected trailing column update event, got %+v", events)
	}
}

func TestColumnHandler_Update_NotFound(t *testing.T) {
	r, _ := setupRlsRouter(t)
	req := httptest.NewRequest("PATCH", "/api/provision/proj-a/column-policies/ghost", rlsJSONBody(t, validColumnPolicy()))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Errorf("update missing: got %d, want 404", w.Code)
	}
}

// -------------------- helpers --------------------

func createPolicy(t *testing.T, r chi.Router, projectID string, body map[string]any) *domain.Policy {
	t.Helper()
	req := httptest.NewRequest("POST", "/api/provision/"+projectID+"/rls-policies/", rlsJSONBody(t, body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("create fixture: got %d body=%s", w.Code, w.Body.String())
	}
	var out domain.Policy
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode fixture: %v", err)
	}
	return &out
}
