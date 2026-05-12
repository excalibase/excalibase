package handler

// Phase 9a — SERIALIZABLE isolation + retry-on-40001 surfaces to callers as
// HTTP 409 Conflict on the public invoke route.
//
// The Deno runtime owns the retry loop itself; once it gives up it returns
// {Status: 409, Body: {"error":"MUTATION_CONFLICT","attempts":N,"code":"40001"}}.
// The Go gateway must pass this straight through PublicInvoke (and the
// admin Invoke path) — same status, same body, no shape mangling.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/edgefn"
	"github.com/go-chi/chi/v5"
)

// mockRuntimeReturning409 returns a runtime stand-in that:
//   - accepts /deploy
//   - returns a Phase-9a-shaped 409 envelope on /invoke/{id}
//
// The Go side never inspects the body; it just must preserve status+body.
func mockRuntimeReturning409(t *testing.T) *httptest.Server {
	t.Helper()
	const conflictBody = `{"error":"MUTATION_CONFLICT","attempts":5,"code":"40001","message":"could not serialize access due to read/write dependencies"}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/health":
			w.Write([]byte(`{"status":"healthy"}`))
		case r.URL.Path == "/deploy" && r.Method == http.MethodPost:
			w.WriteHeader(http.StatusCreated)
			w.Write([]byte(`{"id":"ok"}`))
		case strings.HasPrefix(r.URL.Path, "/invoke/") && r.Method == http.MethodPost:
			// The runtime wraps the user response in an InvokeResponse
			// envelope. The 409 status is on the envelope, the body
			// carries the JSON the user code would have returned.
			json.NewEncoder(w).Encode(edgefn.InvokeResponse{
				Status:  http.StatusConflict,
				Headers: map[string]string{"Content-Type": "application/json"},
				Body:    conflictBody,
			})
		default:
			w.WriteHeader(http.StatusOK)
			w.Write([]byte("{}"))
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// TestPublicInvoke_PassesThrough409Conflict — when the runtime returns a
// Phase-9a 409 Conflict, the Go gateway must surface it unchanged to the
// public caller. This is the contract the front-end / SDK rely on to
// distinguish "retry exhausted under contention" from a generic 500.
func TestPublicInvoke_PassesThrough409Conflict(t *testing.T) {
	store := edgefn.NewFunctionStore(t.TempDir())
	v := newFakeVault()
	secrets := edgefn.NewSecretsStore(v)
	runtime := mockRuntimeReturning409(t)
	client := edgefn.NewRuntimeClient(runtime.URL, "")
	instStore := &inMemoryInstanceStore{insts: map[string]*domain.DatabaseInstance{
		"proj_p9a": {ProjectID: "proj_p9a", OrgID: "default"},
	}}
	h := NewFunctionHandler(store, secrets, client, instStore, nil, testAPIBase)

	verify := false
	if err := store.Save(&edgefn.Function{
		ProjectID: "proj_p9a",
		ID:        "transfer-funds",
		Name:      "Transfer Funds",
		Files:     []edgefn.File{{Path: testIndexTS, Content: testDefaultHandler}},
		Active:    true,
		VerifyJwt: &verify,
	}); err != nil {
		t.Fatalf("save: %v", err)
	}

	pub := chi.NewRouter()
	pub.HandleFunc(testPublicInvokeRoute, h.PublicInvoke)
	req := httptest.NewRequest("POST", "/functions/v1/proj_p9a/transfer-funds", strings.NewReader(`{"args":{}}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	pub.ServeHTTP(w, req)

	if w.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d (body=%s)", w.Code, w.Body.String())
	}

	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("body must be JSON: %v body=%s", err, w.Body.String())
	}
	if body["error"] != "MUTATION_CONFLICT" {
		t.Errorf("error=%v, want MUTATION_CONFLICT (body=%s)", body["error"], w.Body.String())
	}
	if attempts, ok := body["attempts"].(float64); !ok || int(attempts) != 5 {
		t.Errorf("attempts=%v, want 5", body["attempts"])
	}
	if body["code"] != "40001" {
		t.Errorf("code=%v, want 40001", body["code"])
	}
}

// TestPublicInvoke_PassesThrough409Deadlock — same contract, but the
// runtime's last SQLSTATE was 40P01 (deadlock). Body code field reflects.
func TestPublicInvoke_PassesThrough409Deadlock(t *testing.T) {
	store := edgefn.NewFunctionStore(t.TempDir())
	v := newFakeVault()
	secrets := edgefn.NewSecretsStore(v)
	const deadlockBody = `{"error":"MUTATION_CONFLICT","attempts":3,"code":"40P01","message":"deadlock detected"}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/health":
			w.Write([]byte(`{"status":"healthy"}`))
		case r.URL.Path == "/deploy" && r.Method == http.MethodPost:
			w.WriteHeader(http.StatusCreated)
			w.Write([]byte(`{"id":"ok"}`))
		case strings.HasPrefix(r.URL.Path, "/invoke/"):
			json.NewEncoder(w).Encode(edgefn.InvokeResponse{
				Status:  http.StatusConflict,
				Headers: map[string]string{"Content-Type": "application/json"},
				Body:    deadlockBody,
			})
		}
	}))
	t.Cleanup(srv.Close)
	client := edgefn.NewRuntimeClient(srv.URL, "")
	instStore := &inMemoryInstanceStore{insts: map[string]*domain.DatabaseInstance{
		"proj_p9b": {ProjectID: "proj_p9b", OrgID: "default"},
	}}
	h := NewFunctionHandler(store, secrets, client, instStore, nil, testAPIBase)

	verify := false
	if err := store.Save(&edgefn.Function{
		ProjectID: "proj_p9b",
		ID:        "swap-rows",
		Name:      "Swap Rows",
		Files:     []edgefn.File{{Path: testIndexTS, Content: testDefaultHandler}},
		Active:    true,
		VerifyJwt: &verify,
	}); err != nil {
		t.Fatalf("save: %v", err)
	}

	pub := chi.NewRouter()
	pub.HandleFunc(testPublicInvokeRoute, h.PublicInvoke)
	req := httptest.NewRequest("POST", "/functions/v1/proj_p9b/swap-rows", strings.NewReader(`{"args":{}}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	pub.ServeHTTP(w, req)

	if w.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d (body=%s)", w.Code, w.Body.String())
	}
	var body map[string]any
	json.Unmarshal(w.Body.Bytes(), &body)
	if body["code"] != "40P01" {
		t.Errorf("code=%v, want 40P01", body["code"])
	}
	if body["error"] != "MUTATION_CONFLICT" {
		t.Errorf("error=%v, want MUTATION_CONFLICT", body["error"])
	}
}
