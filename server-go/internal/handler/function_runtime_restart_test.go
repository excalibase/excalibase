package handler

import (
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/edgefn"
	"github.com/excalibase/provisioning-poc/internal/k8s"
	"github.com/go-chi/chi/v5"
)

// EXC-569: a secret or allowlist change replaces the project's runtime pod.
// Deploys and invokes in that window must wait for the new pod, a restarted
// runtime that lost its functions must be refilled, and nothing a caller sees
// may name the runtime's address.

const (
	testRestartProject = "proj_restart1"
	testRestartNS      = "default-proj_restart1"
	testRestartBase    = "/api/projects/" + testRestartProject + "/functions"
)

// restartingRuntime is a Deno runtime stand-in that can be down (refusing
// connections at its address) and that records each deploy it receives.
type restartingRuntime struct {
	mu       sync.Mutex
	addr     string
	srv      *httptest.Server
	scripts  map[string]edgefn.DeployRequest
	onDeploy func()
}

func newRestartingRuntime(t *testing.T) *restartingRuntime {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	rt := &restartingRuntime{addr: ln.Addr().String(), scripts: map[string]edgefn.DeployRequest{}}
	ln.Close()
	t.Cleanup(rt.stop)
	return rt
}

func (rt *restartingRuntime) url() string { return "http://" + rt.addr }

func (rt *restartingRuntime) start(t *testing.T) {
	t.Helper()
	var ln net.Listener
	var err error
	for i := 0; i < 50; i++ {
		if ln, err = net.Listen("tcp", rt.addr); err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err != nil {
		t.Errorf("listen on %s: %v", rt.addr, err)
		return
	}
	srv := httptest.NewUnstartedServer(http.HandlerFunc(rt.serve))
	srv.Listener = ln
	srv.Start()
	rt.mu.Lock()
	rt.srv = srv
	rt.mu.Unlock()
}

func (rt *restartingRuntime) startAfter(t *testing.T, delay time.Duration) {
	done := make(chan struct{})
	go func() {
		defer close(done)
		time.Sleep(delay)
		rt.start(t)
	}()
	t.Cleanup(func() { <-done })
}

func (rt *restartingRuntime) stop() {
	rt.mu.Lock()
	srv := rt.srv
	rt.srv = nil
	rt.mu.Unlock()
	if srv != nil {
		srv.Close()
	}
}

// restart drops every function, as a new runtime pod has none.
func (rt *restartingRuntime) restart() {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	rt.scripts = map[string]edgefn.DeployRequest{}
}

func (rt *restartingRuntime) has(id string) bool {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	_, ok := rt.scripts[id]
	return ok
}

func (rt *restartingRuntime) serve(w http.ResponseWriter, r *http.Request) {
	w.Header().Set(sharedContentType, sharedMIMEJSON)
	switch {
	case r.URL.Path == "/health":
		_ = json.NewEncoder(w).Encode(map[string]any{"status": "healthy"})
	case r.URL.Path == "/deploy":
		rt.mu.Lock()
		hook := rt.onDeploy
		serveMockDeploy(w, r, rt.scripts)
		rt.mu.Unlock()
		if hook != nil {
			hook()
		}
	case strings.HasPrefix(r.URL.Path, testInvokePath):
		rt.mu.Lock()
		defer rt.mu.Unlock()
		id := r.URL.Path[len(testInvokePath):]
		if _, ok := rt.scripts[id]; !ok {
			// What the runtime answers for an id it does not hold.
			w.WriteHeader(http.StatusInternalServerError)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "function not found: " + id})
			return
		}
		serveMockInvoke(w, r.URL.Path, rt.scripts)
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

type restartFixture struct {
	router  *chi.Mux
	handler *FunctionHandler
	k8s     *k8s.MockClient
	runtime *restartingRuntime
}

func setupRestartHandler(t *testing.T) restartFixture {
	t.Helper()
	store := edgefn.NewFunctionStore(t.TempDir())
	secrets := edgefn.NewSecretsStore(newFakeVault())
	rt := newRestartingRuntime(t)
	mockK8s := k8s.NewMockClient()
	instStore := &inMemoryInstanceStore{insts: map[string]*domain.DatabaseInstance{
		testRestartProject: {ProjectID: testRestartProject, OrgID: "default", Namespace: testRestartNS},
	}}
	h := NewFunctionHandler(store, secrets, nil, instStore, nil, testAPIBase)
	h.SetK8sClient(mockK8s, testDenoImage, "secret")
	h.SetRuntimeURLFn(func(string) string { return rt.url() })
	h.SetEgressStore(&memEgressStore{hosts: map[string][]string{}})
	h.runtimeWait = runtimeWait{coldStart: 2 * time.Second, rollout: 2 * time.Second, poll: 10 * time.Millisecond, retry: 2 * time.Second}

	r := chi.NewRouter()
	r.Route(testFunctionsRoute, func(r chi.Router) {
		r.Post("/", h.Create)
		r.Post("/secrets", h.SetSecret)
		r.Put("/egress", h.PutEgress)
		r.Get("/runtime/status", h.RuntimeStatus)
		r.Post("/{fnId}/invoke", h.Invoke)
	})
	return restartFixture{router: r, handler: h, k8s: mockK8s, runtime: rt}
}

func restartFnBody(id string) map[string]any {
	return map[string]any{
		"id": id, "name": id,
		"files": []map[string]string{{"path": testIndexTS, "content": testDefaultHandler}},
	}
}

func assertNoInternalAddress(t *testing.T, body, addr string) {
	t.Helper()
	host, port, _ := net.SplitHostPort(addr)
	for _, leak := range []string{addr, host, ":" + port, "dial tcp", "connection refused", "svc.cluster.local", testRestartNS} {
		if strings.Contains(body, leak) {
			t.Fatalf("response names an internal detail %q: %s", leak, body)
		}
	}
}

func TestFunctionDeploy_WaitsForARestartingRuntime(t *testing.T) {
	f := setupRestartHandler(t)
	f.runtime.startAfter(t, 300*time.Millisecond)

	w := doJSON(f.router, http.MethodPost, testRestartBase+"/", restartFnBody("hello"))
	if w.Code != http.StatusCreated {
		t.Fatalf("deploy during a restart: %d %s", w.Code, w.Body.String())
	}
	if !f.runtime.has(testRestartProject + "__hello") {
		t.Fatal("the function never reached the runtime")
	}
}

func TestFunctionDeploy_RuntimeStillDown_SaysSoWithoutItsAddress(t *testing.T) {
	f := setupRestartHandler(t)
	f.handler.runtimeWait.retry = 200 * time.Millisecond

	logged := captureLog(t)
	w := doJSON(f.router, http.MethodPost, testRestartBase+"/", restartFnBody("hello"))
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "restarting") {
		t.Fatalf("body should say the runtime is restarting: %s", w.Body.String())
	}
	if w.Header().Get("Retry-After") == "" {
		t.Fatal("a 503 for a restart should carry Retry-After")
	}
	assertNoInternalAddress(t, w.Body.String(), f.runtime.addr)
	if !strings.Contains(logged.String(), f.runtime.addr) {
		t.Fatalf("the address belongs in the server log, got: %s", logged)
	}
}

func TestFunctionInvoke_RuntimeStillDown_SaysSoWithoutItsAddress(t *testing.T) {
	f := setupRestartHandler(t)
	f.handler.runtimeWait.retry = 200 * time.Millisecond
	f.runtime.start(t)
	if w := doJSON(f.router, http.MethodPost, testRestartBase+"/", restartFnBody("hello")); w.Code != http.StatusCreated {
		t.Fatalf("deploy: %d %s", w.Code, w.Body.String())
	}
	f.runtime.stop()

	w := doJSON(f.router, http.MethodPost, testRestartBase+"/hello/invoke", map[string]any{})
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503: %s", w.Code, w.Body.String())
	}
	assertNoInternalAddress(t, w.Body.String(), f.runtime.addr)
}

// A runtime that restarted has none of the project's functions until the
// periodic replay finds it; the invoke refills the one it needs instead of
// answering 500.
func TestFunctionInvoke_AfterARuntimeRestart_RedeploysAndAnswers(t *testing.T) {
	f := setupRestartHandler(t)
	f.runtime.start(t)
	if w := doJSON(f.router, http.MethodPost, testRestartBase+"/", restartFnBody("hello")); w.Code != http.StatusCreated {
		t.Fatalf("deploy: %d %s", w.Code, w.Body.String())
	}
	f.runtime.restart()

	w := doJSON(f.router, http.MethodPost, testRestartBase+"/hello/invoke", map[string]any{})
	if w.Code != http.StatusOK {
		t.Fatalf("invoke after a restart: %d %s", w.Code, w.Body.String())
	}
	if !f.runtime.has(testRestartProject + "__hello") {
		t.Fatal("the invoke did not put the function back")
	}
}

// rolloutAfter reports the runtime rolling out for the first n polls and
// ready after, and says whether it has reported ready yet.
func rolloutAfter(n int32) (func(string) (k8s.DenoRolloutState, error), *atomic.Bool) {
	var polls atomic.Int32
	ready := &atomic.Bool{}
	return func(string) (k8s.DenoRolloutState, error) {
		if polls.Add(1) > n {
			ready.Store(true)
			return k8s.DenoRuntimeReady, nil
		}
		return k8s.DenoRuntimeRollingOut, nil
	}, ready
}

// The allowlist change rolls the pod. Redeploying to the old pod while the
// new one starts loses every function when the old pod goes.
func TestPutEgress_RedeploysOnlyOnceTheNewRuntimeIsReady(t *testing.T) {
	f := setupRestartHandler(t)
	f.runtime.start(t)
	if w := doJSON(f.router, http.MethodPost, testRestartBase+"/", restartFnBody("hello")); w.Code != http.StatusCreated {
		t.Fatalf("deploy: %d %s", w.Code, w.Body.String())
	}

	rollout, ready := rolloutAfter(5)
	f.k8s.DenoRolloutFunc = rollout
	var early, deploys atomic.Int32
	f.runtime.onDeploy = func() {
		deploys.Add(1)
		if !ready.Load() {
			early.Add(1)
		}
	}

	w := doJSON(f.router, http.MethodPut, testRestartBase+"/egress", map[string]any{"allowedHosts": []string{"api.example.com"}})
	if w.Code != http.StatusOK {
		t.Fatalf("PUT egress: %d %s", w.Code, w.Body.String())
	}
	if deploys.Load() == 0 {
		t.Fatal("the functions were not redeployed")
	}
	if early.Load() != 0 {
		t.Fatalf("%d deploys went to the runtime while it was still rolling out", early.Load())
	}
}

func TestPutEgress_ARolloutThatNeverFinishesIsBounded(t *testing.T) {
	f := setupRestartHandler(t)
	f.runtime.start(t)
	if w := doJSON(f.router, http.MethodPost, testRestartBase+"/", restartFnBody("hello")); w.Code != http.StatusCreated {
		t.Fatalf("deploy: %d %s", w.Code, w.Body.String())
	}
	f.handler.runtimeWait.rollout = 200 * time.Millisecond
	f.k8s.DenoRolloutFunc = func(string) (k8s.DenoRolloutState, error) { return k8s.DenoRuntimeRollingOut, nil }

	start := time.Now()
	w := doJSON(f.router, http.MethodPut, testRestartBase+"/egress", map[string]any{"allowedHosts": []string{"api.example.com"}})
	if w.Code != http.StatusOK {
		t.Fatalf("PUT egress: %d %s", w.Code, w.Body.String())
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("PUT egress took %s with a 200ms rollout budget", elapsed)
	}
}

// A runtime created on this call (or reconciled into a rollout by it) is
// only used once it has rolled out.
func TestFunctionDeploy_ColdRuntimeIsUsedOnlyOnceRolledOut(t *testing.T) {
	f := setupRestartHandler(t)
	f.runtime.start(t)
	rollout, ready := rolloutAfter(5)
	f.k8s.DenoRolloutFunc = rollout
	var early atomic.Int32
	f.runtime.onDeploy = func() {
		if !ready.Load() {
			early.Add(1)
		}
	}

	if w := doJSON(f.router, http.MethodPost, testRestartBase+"/", restartFnBody("hello")); w.Code != http.StatusCreated {
		t.Fatalf("deploy: %d %s", w.Code, w.Body.String())
	}
	if early.Load() != 0 {
		t.Fatal("the deploy went to the runtime before it had rolled out")
	}
}

func TestFunctionDeploy_ColdRuntimeThatNeverRollsOut_NoInternalDetail(t *testing.T) {
	f := setupRestartHandler(t)
	f.runtime.start(t)
	f.handler.runtimeWait.coldStart = 200 * time.Millisecond
	f.k8s.DenoRolloutFunc = func(string) (k8s.DenoRolloutState, error) { return k8s.DenoRuntimeRollingOut, nil }

	w := doJSON(f.router, http.MethodPost, testRestartBase+"/", restartFnBody("hello"))
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503: %s", w.Code, w.Body.String())
	}
	assertNoInternalAddress(t, w.Body.String(), f.runtime.addr)
}

func runtimeStatusOf(t *testing.T, f restartFixture) map[string]any {
	t.Helper()
	w := doJSON(f.router, http.MethodGet, testRestartBase+"/runtime/status", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status: %d %s", w.Code, w.Body.String())
	}
	var out map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestRuntimeStatus_SaysRestartingWhileTheRuntimeRollsOut(t *testing.T) {
	f := setupRestartHandler(t)
	f.runtime.start(t)
	if w := doJSON(f.router, http.MethodPost, testRestartBase+"/", restartFnBody("hello")); w.Code != http.StatusCreated {
		t.Fatalf("deploy: %d %s", w.Code, w.Body.String())
	}
	if got := runtimeStatusOf(t, f); got["status"] != "healthy" || got["healthy"] != true {
		t.Fatalf("settled runtime: %v", got)
	}

	f.k8s.DenoRolloutFunc = func(string) (k8s.DenoRolloutState, error) { return k8s.DenoRuntimeRollingOut, nil }
	if got := runtimeStatusOf(t, f); got["status"] != "restarting" || got["healthy"] != false {
		t.Fatalf("rolling runtime: %v, want restarting", got)
	}
}

func TestRuntimeStatus_UnreachableRuntimeIsNotHealthy(t *testing.T) {
	f := setupRestartHandler(t)
	f.runtime.start(t)
	if w := doJSON(f.router, http.MethodPost, testRestartBase+"/", restartFnBody("hello")); w.Code != http.StatusCreated {
		t.Fatalf("deploy: %d %s", w.Code, w.Body.String())
	}
	f.runtime.stop()
	if got := runtimeStatusOf(t, f); got["healthy"] != false || got["status"] != "unavailable" {
		t.Fatalf("stopped runtime: %v", got)
	}
}

// Saving a secret redeploys every function. A runtime that is restarting
// right then still gets them once it is back.
func TestSetSecret_RedeploysToARestartingRuntime(t *testing.T) {
	f := setupRestartHandler(t)
	f.runtime.start(t)
	if w := doJSON(f.router, http.MethodPost, testRestartBase+"/", restartFnBody("hello")); w.Code != http.StatusCreated {
		t.Fatalf("deploy: %d %s", w.Code, w.Body.String())
	}
	f.runtime.stop()
	f.runtime.restart()
	f.runtime.startAfter(t, 300*time.Millisecond)

	w := doJSON(f.router, http.MethodPost, testRestartBase+"/secrets", map[string]string{"key": "API_KEY", "value": "v1"})
	if w.Code != http.StatusOK {
		t.Fatalf("set secret: %d %s", w.Code, w.Body.String())
	}
	if !f.runtime.has(testRestartProject + "__hello") {
		t.Fatal("the redeploy did not reach the restarted runtime")
	}
}
