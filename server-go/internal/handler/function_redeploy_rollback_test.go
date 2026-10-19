package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/edgefn"
	"github.com/excalibase/provisioning-poc/internal/storage"
	"github.com/go-chi/chi/v5"
)

// functionRouterWithRuntime serves Create against a runtime that refuses every
// deploy once failDeploys is set.
func functionRouterWithRuntime(t *testing.T, failDeploys *atomic.Bool) (chi.Router, edgefn.Store) {
	t.Helper()
	runtime := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/deploy" && failDeploys.Load() {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.Header().Set(sharedContentType, sharedMIMEJSON)
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	}))
	t.Cleanup(runtime.Close)
	store := edgefn.NewFunctionStore(t.TempDir())
	instStore := &inMemoryInstanceStore{insts: map[string]*domain.DatabaseInstance{
		"proj_p1": {ProjectID: "proj_p1", OrgID: "default"},
	}}
	var orgStore storage.OrgStore
	h := NewFunctionHandler(store, edgefn.NewSecretsStore(newFakeVault()), edgefn.NewRuntimeClient(runtime.URL, ""),
		instStore, orgStore, testAPIBase)
	router := chi.NewRouter()
	router.Post("/api/projects/{projectId}/functions", h.Create)
	return router, store
}

func deployFunction(router chi.Router, content string) *httptest.ResponseRecorder {
	body := `{"id":"hello","name":"Hello","files":[{"path":"index.ts","content":"` + content + `"}]}`
	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest("POST", "/api/projects/proj_p1/functions", strings.NewReader(body)))
	return w
}

// EXC-555: a failed redeploy must not delete the working version the runtime
// still serves; the stored record goes back to it.
func TestCreateFunction_FailedRedeployKeepsThePreviousVersion(t *testing.T) {
	var failDeploys atomic.Bool
	router, store := functionRouterWithRuntime(t, &failDeploys)

	if w := deployFunction(router, "export default () => new Response('v1')"); w.Code != http.StatusCreated {
		t.Fatalf("first deploy: %d %s", w.Code, w.Body.String())
	}
	failDeploys.Store(true)
	if w := deployFunction(router, "export default () => new Response('v2')"); w.Code != http.StatusBadGateway {
		t.Fatalf("redeploy: %d %s, want 502", w.Code, w.Body.String())
	}

	fn, err := store.Get("proj_p1", "hello")
	if err != nil || fn == nil {
		t.Fatalf("the function was deleted by a failed redeploy (err=%v)", err)
	}
	if len(fn.Files) == 0 || !strings.Contains(fn.Files[0].Content, "v1") {
		t.Errorf("stored version is not the previous one: %+v", fn.Files)
	}
}

// A first deploy that fails leaves nothing behind.
func TestCreateFunction_FailedFirstDeployLeavesNoRecord(t *testing.T) {
	var failDeploys atomic.Bool
	failDeploys.Store(true)
	router, store := functionRouterWithRuntime(t, &failDeploys)

	if w := deployFunction(router, "export default () => new Response('v1')"); w.Code != http.StatusBadGateway {
		t.Fatalf("deploy: %d %s, want 502", w.Code, w.Body.String())
	}
	if fn, _ := store.Get("proj_p1", "hello"); fn != nil {
		t.Errorf("a failed first deploy kept the function")
	}
}

// EXC-640: a deploy the runtime refuses for the function's own code answers
// 400 with the runtime's reason, not 502.
func TestCreateFunction_RefusedCodeAnswers400WithTheReason(t *testing.T) {
	reason := "index.ts must use export default (req: Request) => Response; found only named exports: invite"
	runtime := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set(sharedContentType, sharedMIMEJSON)
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": reason})
	}))
	t.Cleanup(runtime.Close)
	store := edgefn.NewFunctionStore(t.TempDir())
	instStore := &inMemoryInstanceStore{insts: map[string]*domain.DatabaseInstance{
		"proj_p1": {ProjectID: "proj_p1", OrgID: "default"},
	}}
	h := NewFunctionHandler(store, edgefn.NewSecretsStore(newFakeVault()), edgefn.NewRuntimeClient(runtime.URL, ""),
		instStore, nil, testAPIBase)
	router := chi.NewRouter()
	router.Post("/api/projects/{projectId}/functions", h.Create)

	w := deployFunction(router, "export function invite() {}")
	var refusal struct {
		Error string `json:"error"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &refusal)
	if w.Code != http.StatusBadRequest || !strings.Contains(refusal.Error, reason) {
		t.Fatalf("deploy: %d %s, want 400 naming %q", w.Code, w.Body.String(), reason)
	}
	if fn, _ := store.Get("proj_p1", "hello"); fn != nil {
		t.Errorf("a refused first deploy kept the function")
	}
}

// EXC-640: a stored function the runtime no longer holds and refuses to load
// again answers with the refusal, not a bare "internal error".
func TestInvokeFunction_RefusedRedeployAnswersTheReason(t *testing.T) {
	reason := "index.ts must use export default (req: Request) => Response; found only named exports: invite"
	runtime := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set(sharedContentType, sharedMIMEJSON)
		if r.URL.Path == "/deploy" {
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": reason})
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(runtime.Close)
	store := edgefn.NewFunctionStore(t.TempDir())
	if err := store.Save(&edgefn.Function{ID: "hello", ProjectID: "proj_p1", Name: "hello", Active: true,
		Files: []edgefn.File{{Path: "index.ts", Content: "export function invite() {}"}}}); err != nil {
		t.Fatal(err)
	}
	instStore := &inMemoryInstanceStore{insts: map[string]*domain.DatabaseInstance{
		"proj_p1": {ProjectID: "proj_p1", OrgID: "default"},
	}}
	h := NewFunctionHandler(store, edgefn.NewSecretsStore(newFakeVault()), edgefn.NewRuntimeClient(runtime.URL, ""),
		instStore, nil, testAPIBase)
	router := chi.NewRouter()
	router.Post("/api/projects/{projectId}/functions/{fnId}/invoke", h.Invoke)

	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest("POST", "/api/projects/proj_p1/functions/hello/invoke", strings.NewReader("{}")))
	var answer struct {
		Error string `json:"error"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &answer)
	if w.Code != http.StatusInternalServerError || answer.Error != "the function cannot run: "+reason {
		t.Fatalf("invoke: %d %s", w.Code, w.Body.String())
	}
}
