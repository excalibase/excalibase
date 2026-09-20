package handler

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/edgefn"
	"github.com/excalibase/provisioning-poc/internal/storage"
	"github.com/go-chi/chi/v5"
)

// setupMetadataHandler wires a FunctionHandler with two pre-populated v2
// functions. Returns the chi router, the underlying store, and the handler.
func setupMetadataHandler(t *testing.T) (*chi.Mux, *edgefn.FunctionStore, *FunctionHandler) {
	t.Helper()
	dir := t.TempDir()
	store := edgefn.NewFunctionStore(dir)
	v := newFakeVault()
	secrets := edgefn.NewSecretsStore(v)
	runtime, _ := mockFnRuntime(t)
	client := edgefn.NewRuntimeClient(runtime.URL, "test-runtime-secret")

	instStore := &inMemoryInstanceStore{insts: map[string]*domain.DatabaseInstance{
		"proj_p1": {ProjectID: "proj_p1", OrgID: "default"},
	}}
	var orgStore storage.OrgStore
	h := NewFunctionHandler(store, secrets, client, instStore, orgStore, testAPIBase)
	// Wire the runtime secret directly — the internal callback authenticates
	// with this same shared secret. SetK8sClient is the canonical setter.
	h.runtimeSecret = "test-runtime-secret"

	// Pre-populate two v2 functions so the metadata endpoint has something
	// to return. Save() runs Validate() + Bundle(), which sets RuntimeShape.
	for _, f := range []*edgefn.Function{
		{
			ProjectID: "proj_p1",
			ID:        "users",
			Name:      "users",
			Files: []edgefn.File{{Path: "index.ts", Content: `
export default {
  kind: "query",
  args: { parse: (a) => a },
  handler: async () => ({ ok: true }),
}`}},
		},
		{
			ProjectID: "proj_p1",
			ID:        "tasks",
			Name:      "tasks",
			Files: []edgefn.File{{Path: "index.ts", Content: `
export default {
  kind: "mutation",
  args: { parse: (a) => a },
  handler: async () => ({ id: 1 }),
}`}},
		},
	} {
		if err := store.Save(f); err != nil {
			t.Fatalf("seed save: %v", err)
		}
	}

	r := chi.NewRouter()
	r.Route(testFunctionsRoute, func(r chi.Router) {
		r.Get("/_metadata", h.ListExportMetadata)
		r.Route("/{fnId}", func(r chi.Router) {
			r.Get("/", h.Get)
		})
	})
	// Internal callback route (no JWT — uses the runtime shared secret).
	r.Post("/internal/runtime/functions/{fnId}/metadata", h.ReceiveExportMetadata)
	return r, store, h
}

// --- GET /_metadata ---

func TestExportMetadata_ListsProjectFunctions(t *testing.T) {
	r, store, _ := setupMetadataHandler(t)

	// Stamp some export metadata on the stored records so the response is
	// non-trivial. In production this gets set by the internal callback.
	users, _ := store.Get("proj_p1", "users")
	users.ExportMetadata = json.RawMessage(`[{"name":"list","kind":"query","argsJsonSchema":{"type":"object","properties":{"status":{"type":"string"}}}}]`)
	if err := store.Save(users); err != nil {
		t.Fatalf("save users: %v", err)
	}

	req := httptest.NewRequest("GET", "/api/projects/proj_p1/functions/_metadata", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("GET _metadata: %d body=%s", w.Code, w.Body.String())
	}

	var out []map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v body=%s", err, w.Body.String())
	}
	if len(out) != 2 {
		t.Fatalf("want 2 functions, got %d: %+v", len(out), out)
	}
	// Map by name so the assertion is order-independent.
	byName := map[string]map[string]interface{}{}
	for _, fn := range out {
		byName[fn["name"].(string)] = fn
	}
	usersOut := byName["users"]
	if usersOut["runtimeShape"] != "v2" {
		t.Errorf("users runtimeShape: %v", usersOut["runtimeShape"])
	}
	exports, ok := usersOut["exports"].([]interface{})
	if !ok || len(exports) != 1 {
		t.Fatalf("users exports: %+v", usersOut["exports"])
	}
	exp0 := exports[0].(map[string]interface{})
	if exp0["name"] != "list" {
		t.Errorf("export[0] name: %v", exp0["name"])
	}
	// Even the empty-metadata function should appear with exports: [].
	tasksOut := byName["tasks"]
	if tasksOut["exports"] == nil {
		t.Errorf("tasks should have exports field (empty array) even with no metadata")
	}
}

func TestExportMetadata_UnknownProjectReturnsEmptyList(t *testing.T) {
	r, _, _ := setupMetadataHandler(t)

	req := httptest.NewRequest("GET", "/api/projects/proj_unknown/functions/_metadata", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("GET _metadata for unknown project: %d", w.Code)
	}
	var out []interface{}
	json.Unmarshal(w.Body.Bytes(), &out)
	if len(out) != 0 {
		t.Errorf("want empty list for unknown project, got %d entries", len(out))
	}
}

func TestExportMetadata_RejectsMalformedProjectID(t *testing.T) {
	r, _, _ := setupMetadataHandler(t)

	req := httptest.NewRequest("GET", "/api/projects/has..bad/functions/_metadata", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("want 400 for malformed project id, got %d", w.Code)
	}
}

// --- POST /internal/runtime/functions/{fnId}/metadata ---

func TestReceiveExportMetadata_UpdatesStoredFunction(t *testing.T) {
	r, store, h := setupMetadataHandler(t)

	payload := map[string]interface{}{
		"projectId": "proj_p1",
		"exports": []map[string]interface{}{
			{
				"name":           "list",
				"kind":           "query",
				"argsJsonSchema": map[string]interface{}{"type": "object"},
			},
			{
				"name":           "create",
				"kind":           "mutation",
				"argsJsonSchema": map[string]interface{}{"type": "object"},
			},
		},
	}
	body, _ := json.Marshal(payload)
	req := httptest.NewRequest("POST", "/internal/runtime/functions/users/metadata", bytes.NewReader(body))
	req.Header.Set("X-Excalibase-Runtime-Token", edgefn.DeriveRuntimeSecret(h.runtimeSecret, "proj_p1"))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK && w.Code != http.StatusNoContent {
		t.Fatalf("POST callback: %d body=%s", w.Code, w.Body.String())
	}

	updated, _ := store.Get("proj_p1", "users")
	if updated == nil {
		t.Fatal("function disappeared after callback")
	}
	if len(updated.ExportMetadata) == 0 {
		t.Fatal("ExportMetadata was not persisted")
	}
	// Round-trip through JSON to assert the shape was preserved.
	var decoded []map[string]interface{}
	if err := json.Unmarshal(updated.ExportMetadata, &decoded); err != nil {
		t.Fatalf("decode persisted ExportMetadata: %v raw=%s", err, string(updated.ExportMetadata))
	}
	if len(decoded) != 2 {
		t.Fatalf("want 2 exports, got %d", len(decoded))
	}
	if decoded[0]["name"] != "list" {
		t.Errorf("decoded[0].name: %v", decoded[0]["name"])
	}
}

func TestReceiveExportMetadata_RejectsMissingRuntimeToken(t *testing.T) {
	r, _, _ := setupMetadataHandler(t)
	body, _ := json.Marshal(map[string]interface{}{"projectId": "proj_p1", "exports": []interface{}{}})
	req := httptest.NewRequest("POST", "/internal/runtime/functions/users/metadata", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	// No X-Excalibase-Runtime-Token header.
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized && w.Code != http.StatusForbidden {
		t.Errorf("want 401/403 without runtime token, got %d", w.Code)
	}
}

func TestReceiveExportMetadata_RejectsWrongRuntimeToken(t *testing.T) {
	r, _, _ := setupMetadataHandler(t)
	body, _ := json.Marshal(map[string]interface{}{"projectId": "proj_p1", "exports": []interface{}{}})
	req := httptest.NewRequest("POST", "/internal/runtime/functions/users/metadata", bytes.NewReader(body))
	req.Header.Set("X-Excalibase-Runtime-Token", "wrong-secret")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized && w.Code != http.StatusForbidden {
		t.Errorf("want 401/403 for wrong runtime token, got %d", w.Code)
	}
}

func TestReceiveExportMetadata_UnknownFunctionReturns404(t *testing.T) {
	r, _, h := setupMetadataHandler(t)
	body, _ := json.Marshal(map[string]interface{}{"projectId": "proj_p1", "exports": []interface{}{}})
	req := httptest.NewRequest("POST", "/internal/runtime/functions/ghost/metadata", bytes.NewReader(body))
	req.Header.Set("X-Excalibase-Runtime-Token", edgefn.DeriveRuntimeSecret(h.runtimeSecret, "proj_p1"))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Errorf("want 404 for unknown function, got %d body=%s", w.Code, w.Body.String())
	}
}

func TestReceiveExportMetadata_RejectsBadJSON(t *testing.T) {
	r, _, h := setupMetadataHandler(t)
	req := httptest.NewRequest("POST", "/internal/runtime/functions/users/metadata", strings.NewReader("not json"))
	req.Header.Set("X-Excalibase-Runtime-Token", edgefn.DeriveRuntimeSecret(h.runtimeSecret, "proj_p1"))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("want 400 for malformed JSON body, got %d", w.Code)
	}
}

func TestReceiveExportMetadata_RejectsMalformedProjectID(t *testing.T) {
	r, _, h := setupMetadataHandler(t)
	body, _ := json.Marshal(map[string]interface{}{"projectId": "has..bad", "exports": []interface{}{}})
	req := httptest.NewRequest("POST", "/internal/runtime/functions/users/metadata", bytes.NewReader(body))
	req.Header.Set("X-Excalibase-Runtime-Token", edgefn.DeriveRuntimeSecret(h.runtimeSecret, "proj_p1"))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("want 400 for malformed projectId, got %d", w.Code)
	}
}

func TestReceiveExportMetadata_RejectsNonArrayExports(t *testing.T) {
	r, _, h := setupMetadataHandler(t)
	body := []byte(`{"projectId":"proj_p1","exports":{"not":"an array"}}`)
	req := httptest.NewRequest("POST", "/internal/runtime/functions/users/metadata", bytes.NewReader(body))
	req.Header.Set("X-Excalibase-Runtime-Token", edgefn.DeriveRuntimeSecret(h.runtimeSecret, "proj_p1"))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("want 400 for non-array exports, got %d", w.Code)
	}
}

func TestReceiveExportMetadata_EmptyBodyDefaultsToEmptyArray(t *testing.T) {
	r, store, h := setupMetadataHandler(t)
	// Send a body that decodes (valid JSON) but omits "exports". Handler
	// should default to []. Function record gets ExportMetadata = []
	body := []byte(`{"projectId":"proj_p1"}`)
	req := httptest.NewRequest("POST", "/internal/runtime/functions/users/metadata", bytes.NewReader(body))
	req.Header.Set("X-Excalibase-Runtime-Token", edgefn.DeriveRuntimeSecret(h.runtimeSecret, "proj_p1"))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK && w.Code != http.StatusNoContent {
		t.Fatalf("empty-exports body: %d", w.Code)
	}
	updated, _ := store.Get("proj_p1", "users")
	if string(updated.ExportMetadata) != "[]" {
		t.Errorf("ExportMetadata: got %q, want %q", string(updated.ExportMetadata), "[]")
	}
}

func TestReceiveExportMetadata_RejectsBadFunctionID(t *testing.T) {
	r, _, h := setupMetadataHandler(t)
	body, _ := json.Marshal(map[string]interface{}{"projectId": "proj_p1", "exports": []interface{}{}})
	req := httptest.NewRequest("POST", "/internal/runtime/functions/bad..id/metadata", bytes.NewReader(body))
	req.Header.Set("X-Excalibase-Runtime-Token", edgefn.DeriveRuntimeSecret(h.runtimeSecret, "proj_p1"))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("want 400 for malformed function id, got %d", w.Code)
	}
}

// --- helper test ---

func TestExportMetadata_ResponseShapeMatchesSDKContract(t *testing.T) {
	// Sanity test for the wire contract — the SDK codegen reads:
	//   [{ id, name, runtimeShape, exports: [{name,kind,argsJsonSchema}], lastDeployedAt }]
	r, _, _ := setupMetadataHandler(t)
	req := httptest.NewRequest("GET", "/api/projects/proj_p1/functions/_metadata", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	var out []map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	for _, fn := range out {
		for _, k := range []string{"id", "name", "runtimeShape", "exports", "lastDeployedAt"} {
			if _, ok := fn[k]; !ok {
				t.Errorf("missing key %q in metadata response entry: %+v", k, fn)
			}
		}
	}
}

// --- Bundle preamble injection ---

func TestBundle_InjectsMetadataCollectorPreamble(t *testing.T) {
	// Bundle() must inject a marker the worker can read to know it should
	// scan its loaded exports and post a metadata message back to main.
	fn := &edgefn.Function{
		ProjectID: "proj_test0001",
		ID:        "metaq",
		Name:      "Meta Q",
		Files: []edgefn.File{{Path: "index.ts", Content: `
export default {
  kind: "query",
  args: { parse: (a) => a },
  handler: async (ctx, args) => ({ ok: true }),
}`}},
	}
	code, err := fn.Bundle()
	if err != nil {
		t.Fatalf("Bundle: %v", err)
	}
	// The collector preamble exposes a sentinel that the runtime's worker
	// template inspects to decide whether to scan + report metadata. The
	// exact identifier is __excalibase_export_metadata.
	if !strings.Contains(code, "__excalibase_export_metadata") {
		t.Errorf("bundle output missing metadata collector sentinel:\n%s", code)
	}
}

// EXC-418: the runtime callback names its project in the body and its token
// binds it to that project. A function id belonging to some other project must
// not be reachable through it, and a token for another project must not
// authenticate at all.
func TestReceiveExportMetadata_RefusesAFunctionOfAnotherProject(t *testing.T) {
	r, store, _ := setupMetadataHandler(t)
	if err := store.Save(&edgefn.Function{
		ProjectID: "proj_p2",
		ID:        "elsewhere",
		Name:      "elsewhere",
		Files:     []edgefn.File{{Path: "index.ts", Content: "export default { kind: \"query\", args: { parse: (a) => a }, handler: async () => ({}) }"}},
	}); err != nil {
		t.Fatalf("seed other project: %v", err)
	}

	// proj_p1's runtime token, pointed at proj_p2's function.
	req := httptest.NewRequest("POST", "/internal/runtime/functions/elsewhere/metadata",
		bytes.NewBufferString(`{"projectId":"proj_p1","exports":[]}`))
	req.Header.Set("X-Excalibase-Runtime-Token", edgefn.DeriveRuntimeSecret("test-runtime-secret", "proj_p1"))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("got %d (%s), want 404", w.Code, strings.TrimSpace(w.Body.String()))
	}

	other, err := store.Get("proj_p2", "elsewhere")
	if err != nil || other == nil {
		t.Fatalf("read back other project's function: %v", err)
	}
	if len(other.ExportMetadata) != 0 {
		t.Fatalf("the other project's function was written: %s", other.ExportMetadata)
	}
}

func TestReceiveExportMetadata_RefusesATokenMintedForAnotherProject(t *testing.T) {
	r, _, _ := setupMetadataHandler(t)
	req := httptest.NewRequest("POST", "/internal/runtime/functions/users/metadata",
		bytes.NewBufferString(`{"projectId":"proj_p1","exports":[]}`))
	req.Header.Set("X-Excalibase-Runtime-Token", edgefn.DeriveRuntimeSecret("test-runtime-secret", "proj_p2"))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("got %d (%s), want 401", w.Code, strings.TrimSpace(w.Body.String()))
	}
}
