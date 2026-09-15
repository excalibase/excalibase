package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/edgefn"
	"github.com/excalibase/provisioning-poc/internal/k8s"
)

const (
	testReplayProject = "proj_p1"
	testReplayNS      = "default-proj_p1"
)

// restartableRuntime is a mock Deno runtime whose bootId + loaded scripts can
// be reset to simulate a pod restart.
type restartableRuntime struct {
	mu      sync.Mutex
	bootID  string
	scripts map[string]edgefn.DeployRequest
	srv     *httptest.Server
}

func newRestartableRuntime(t *testing.T, bootID string) *restartableRuntime {
	t.Helper()
	rt := &restartableRuntime{bootID: bootID, scripts: map[string]edgefn.DeployRequest{}}
	rt.srv = httptest.NewServer(http.HandlerFunc(rt.serve))
	t.Cleanup(rt.srv.Close)
	return rt
}

func (rt *restartableRuntime) serve(w http.ResponseWriter, r *http.Request) {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	w.Header().Set(sharedContentType, sharedMIMEJSON)
	switch {
	case r.URL.Path == "/health":
		json.NewEncoder(w).Encode(map[string]any{
			"status": "healthy", "scripts": len(rt.scripts), "bootId": rt.bootID,
		})
	case r.URL.Path == "/deploy" && r.Method == http.MethodPost:
		serveMockDeploy(w, r, rt.scripts)
	default:
		w.WriteHeader(404)
	}
}

// restart forgets every script and mints a new bootId — what a pod restart does.
func (rt *restartableRuntime) restart(bootID string) {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	rt.bootID = bootID
	rt.scripts = map[string]edgefn.DeployRequest{}
}

// preload marks ids as loaded with empty placeholders, so a later deploy is
// distinguishable from the pre-existing entry.
func (rt *restartableRuntime) preload(ids ...string) {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	rt.scripts = map[string]edgefn.DeployRequest{}
	for _, id := range ids {
		rt.scripts[id] = edgefn.DeployRequest{}
	}
}

func (rt *restartableRuntime) loaded() []string {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	ids := make([]string, 0, len(rt.scripts))
	for id := range rt.scripts {
		ids = append(ids, id)
	}
	return ids
}

func (rt *restartableRuntime) script(id string) (edgefn.DeployRequest, bool) {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	req, ok := rt.scripts[id]
	return req, ok
}

// replayK8s embeds the interface so any k8s call other than the ones we
// override panics — the replay path must never touch the cluster.
type replayK8s struct {
	k8s.KubeClient
	ensured int
}

func (f *replayK8s) EnsureDenoRuntime(context.Context, string, k8s.DenoRuntimeSpec) error {
	f.ensured++
	return nil
}

func newReplayHandler(t *testing.T, rt *restartableRuntime) (*FunctionHandler, *edgefn.FunctionStore, *replayK8s) {
	t.Helper()
	store := edgefn.NewFunctionStore(t.TempDir())
	secrets := edgefn.NewSecretsStore(newFakeVault())
	instStore := &inMemoryInstanceStore{insts: map[string]*domain.DatabaseInstance{
		testReplayProject: {ProjectID: testReplayProject, OrgID: "default", Namespace: testReplayNS},
	}}
	h := NewFunctionHandler(store, secrets, nil, instStore, nil, testAPIBase)
	fake := &replayK8s{}
	h.SetK8sClient(fake, testDenoImage, "master-secret")
	h.SetRuntimeURLFn(func(string) string { return rt.srv.URL })
	return h, store, fake
}

func saveReplayFn(t *testing.T, store *edgefn.FunctionStore, id string, schema string) {
	t.Helper()
	fn := &edgefn.Function{ID: id, ProjectID: testReplayProject, Name: id, Active: true,
		Files: []edgefn.File{{Path: testIndexTS, Content: testDefaultHandler}}}
	if schema != "" {
		fn.SchemaJSON = json.RawMessage(schema)
	}
	if err := store.Save(fn); err != nil {
		t.Fatal(err)
	}
}

func TestFunctionHandler_ReplayDeploys_BuildsEveryFunctionWithEnvAndSchema(t *testing.T) {
	rt := newRestartableRuntime(t, "boot-1")
	h, store, _ := newReplayHandler(t, rt)
	saveReplayFn(t, store, "plain", "")
	saveReplayFn(t, store, "typed", `{"tables":{}}`)
	if err := h.secrets.Set(testReplayProject, "API_KEY", "k-123"); err != nil {
		t.Fatal(err)
	}

	deploys, err := h.ReplayDeploys(context.Background(), testReplayProject)
	if err != nil {
		t.Fatal(err)
	}
	if len(deploys) != 2 {
		t.Fatalf("deploys: got %d", len(deploys))
	}
	byID := map[string]edgefn.DeployRequest{}
	for _, d := range deploys {
		byID[d.ID] = d
	}
	plain, ok := byID["proj_p1__plain"]
	if !ok || strings.Contains(plain.Code, "__excalibase_function_metadata") {
		t.Fatalf("plain function missing or carries a schema preamble: %+v", plain)
	}
	typed, ok := byID["proj_p1__typed"]
	if !ok || !strings.HasPrefix(typed.Code, `globalThis.__excalibase_function_metadata = { schemaJson: {"tables":{}} };`) {
		t.Fatalf("typed function missing its schema preamble: %.120s", typed.Code)
	}
	if plain.Secrets["API_KEY"] != "k-123" || plain.Secrets["EXCALIBASE_PROJECT_ID"] != testReplayProject {
		t.Fatalf("secrets not merged: %v", plain.Secrets)
	}
}

func TestFunctionHandler_ReplayDeploys_EmptyProject(t *testing.T) {
	rt := newRestartableRuntime(t, "boot-1")
	h, _, _ := newReplayHandler(t, rt)
	deploys, err := h.ReplayDeploys(context.Background(), testReplayProject)
	if err != nil || len(deploys) != 0 {
		t.Fatalf("expected no deploys, got %v %v", deploys, err)
	}
}

func TestFunctionHandler_ReplayRuntimeFor_NeverProvisions(t *testing.T) {
	rt := newRestartableRuntime(t, "boot-1")
	h, _, fake := newReplayHandler(t, rt)

	target, err := h.ReplayRuntimeFor(context.Background(), testReplayProject)
	if err != nil {
		t.Fatal(err)
	}
	status, err := target.Status(context.Background())
	if err != nil || status.BootID != "boot-1" {
		t.Fatalf("status: %+v %v", status, err)
	}
	if fake.ensured != 0 {
		t.Fatalf("replay path provisioned a runtime (%d EnsureDenoRuntime calls)", fake.ensured)
	}
}

func TestFunctionHandler_ReplayRuntimeFor_UnknownProjectErrors(t *testing.T) {
	rt := newRestartableRuntime(t, "boot-1")
	h, _, _ := newReplayHandler(t, rt)
	if _, err := h.ReplayRuntimeFor(context.Background(), "proj_unknown"); err == nil {
		t.Fatal("expected error for project without namespace")
	}
}

func TestFunctionHandler_ReplayRuntimeFor_SharedModeUsesSharedClient(t *testing.T) {
	rt := newRestartableRuntime(t, "boot-1")
	store := edgefn.NewFunctionStore(t.TempDir())
	h := NewFunctionHandler(store, edgefn.NewSecretsStore(newFakeVault()),
		edgefn.NewRuntimeClient(rt.srv.URL, ""), nil, nil, testAPIBase)

	target, err := h.ReplayRuntimeFor(context.Background(), testReplayProject)
	if err != nil {
		t.Fatal(err)
	}
	if status, _ := target.Status(context.Background()); status.BootID != "boot-1" {
		t.Fatalf("shared client not used: %+v", status)
	}
}

func TestFunctionHandler_ReplayRuntimeFor_NoRuntimeConfiguredErrors(t *testing.T) {
	h := NewFunctionHandler(edgefn.NewFunctionStore(t.TempDir()), nil, nil, nil, nil, testAPIBase)
	if _, err := h.ReplayRuntimeFor(context.Background(), testReplayProject); err == nil {
		t.Fatal("expected error when no runtime is configured")
	}
}

// End to end through the handler: functions deployed, runtime restarts, one
// Tick brings every function back with the same code the runtime had before.
func TestFunctionHandler_Replayer_RestoresFunctionsAfterRuntimeRestart(t *testing.T) {
	rt := newRestartableRuntime(t, "boot-1")
	h, store, fake := newReplayHandler(t, rt)
	saveReplayFn(t, store, "alpha", "")
	saveReplayFn(t, store, "beta", `{"tables":{}}`)

	// The two simulated restarts below are microseconds apart; the production
	// MinGap would (correctly) throttle the second one.
	replayer, err := h.NewReplayer(edgefn.ReplayConfig{MinGap: time.Nanosecond})
	if err != nil {
		t.Fatal(err)
	}
	// Cold runtime at first sight: replayed.
	replayer.Tick(context.Background())
	if got := rt.loaded(); len(got) != 2 {
		t.Fatalf("after first tick: loaded %v", got)
	}
	before, _ := rt.script("proj_p1__beta")

	// Steady state: nothing re-sent.
	rt.preload("proj_p1__alpha", "proj_p1__beta")
	replayer.Tick(context.Background())
	if placeholder, ok := rt.script("proj_p1__beta"); !ok || placeholder.Code != "" {
		t.Fatal("steady-state tick re-deployed")
	}

	// Pod restart: new bootId, empty runtime → replayed again, same bundle.
	rt.restart("boot-2")
	replayer.Tick(context.Background())
	after, ok := rt.script("proj_p1__beta")
	if !ok || after.Code != before.Code || after.Secrets["EXCALIBASE_PROJECT_ID"] != testReplayProject {
		t.Fatalf("beta not restored identically after restart: ok=%v", ok)
	}
	if fake.ensured != 0 {
		t.Fatalf("replay provisioned runtimes: %d", fake.ensured)
	}
}

func TestFunctionHandler_NewReplayer_RequiresProjectLister(t *testing.T) {
	h := NewFunctionHandler(&storeWithoutProjects{}, nil, nil, nil, nil, testAPIBase)
	if _, err := h.NewReplayer(edgefn.ReplayConfig{}); err == nil {
		t.Fatal("expected error for a store that cannot list projects")
	}
}

type storeWithoutProjects struct{ edgefn.Store }
