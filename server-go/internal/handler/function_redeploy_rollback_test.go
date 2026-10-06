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
