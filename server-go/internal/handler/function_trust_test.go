package handler

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/excalibase/provisioning-poc/internal/edgefn"
	"github.com/excalibase/provisioning-poc/internal/k8s"
	"github.com/excalibase/provisioning-poc/internal/trust"
)

// EXC-899: a limited owner gets no new function runtime and no function pushed
// into one, by any path: deploy, a missing runtime on invoke, a secret or
// egress save, a re-deploy of a function the runtime lost, or the replay.

type ownerState struct{ state trust.State }

func (o ownerState) ForProject(context.Context, string) (trust.State, error) { return o.state, nil }

func trustGateOf(state trust.State) *trust.Gate {
	return trust.NewGate(ownerState{state}, trust.Policy{MinAgeDays: 90})
}

func ensureCalls(kube *k8s.MockClient) int {
	n := 0
	for _, call := range kube.Calls {
		if strings.HasPrefix(call, "EnsureDenoRuntime") {
			n++
		}
	}
	return n
}

// clusterHandler is a per-project (cluster) handler whose runtime does not exist yet.
func clusterHandler(t *testing.T, state trust.State) (*FunctionHandler, *k8s.MockClient) {
	t.Helper()
	h, _ := suspendedInvokeHandler(t)
	h.SetOrgGuard(nil)
	kube := k8s.NewMockClient()
	h.SetK8sClient(kube, "deno:test", "secret")
	h.clientMu.Lock()
	delete(h.clients, "proj_p1")
	h.clientMu.Unlock()
	h.SetContainerGate(trustGateOf(state))
	return h, kube
}

func publicInvokeRouter(h *FunctionHandler) http.Handler {
	r := chi.NewRouter()
	r.HandleFunc(testPublicInvokeRoute, h.PublicInvoke)
	return r
}

func TestRuntimeClientFor_NoNewRuntimeForALimitedOwner(t *testing.T) {
	h, kube := clusterHandler(t, trust.Limited)
	if _, err := h.runtimeClientFor(context.Background(), "proj_p1"); !errors.Is(err, trust.ErrVerificationRequired) {
		t.Fatalf("runtimeClientFor err = %v, want ErrVerificationRequired", err)
	}
	if ensureCalls(kube) != 0 {
		t.Errorf("a runtime was created: %v", kube.Calls)
	}
}

func TestRuntimeClientFor_AVerifiedOwnerGetsItsRuntime(t *testing.T) {
	h, kube := clusterHandler(t, trust.Full)
	h.runtimeWait.coldStart = 0
	_, _ = h.runtimeClientFor(context.Background(), "proj_p1")
	if ensureCalls(kube) != 1 {
		t.Fatalf("a verified owner's runtime was not ensured: %v", kube.Calls)
	}
}

// Already-running pods are a separate question: an existing runtime is not refused here.
func TestEnsureRuntime_AnExistingRuntimeIsLeftToTheOwnerQuestion(t *testing.T) {
	h, kube := clusterHandler(t, trust.Limited)
	kube.DenoRuntimes = map[string]bool{"ns-1": true}
	h.runtimeWait.coldStart = 0
	if err := h.ensureRuntime(context.Background(), "proj_p1", "ns-1", 0); errors.Is(err, trust.ErrVerificationRequired) {
		t.Fatalf("an existing runtime was refused: %v", err)
	}
}

func TestPublicInvoke_ALimitedOwnersMissingRuntimeIsNotRecreated(t *testing.T) {
	h, kube := clusterHandler(t, trust.Limited)
	w := httptest.NewRecorder()
	publicInvokeRouter(h).ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/functions/v1/proj_p1/hello", nil))
	if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), `"code":"trust_required"`) {
		t.Fatalf("public invoke: %d %s, want 403 trust_required", w.Code, w.Body.String())
	}
	if ensureCalls(kube) != 0 {
		t.Errorf("a runtime was created: %v", kube.Calls)
	}
}

func TestRedeployAll_NotRunForALimitedOwner(t *testing.T) {
	var runtimeCalls atomic.Int32
	runtime := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { runtimeCalls.Add(1) }))
	t.Cleanup(runtime.Close)
	h, _ := newFunctionHandlerFor(t, runtime.URL)
	h.SetContainerGate(trustGateOf(trust.Limited))
	if err := h.store.Save(&edgefn.Function{ID: "hello", Name: "Hello", ProjectID: "proj_p1", Active: true,
		Files: []edgefn.File{{Path: "index.ts", Content: "export default () => new Response('x')"}}}); err != nil {
		t.Fatal(err)
	}
	if err := h.redeployAll(httptest.NewRequest("POST", "/", nil), "proj_p1"); err != nil {
		t.Errorf("a skipped redeploy reported a failure: %v", err)
	}
	if runtimeCalls.Load() != 0 {
		t.Errorf("redeployAll called the runtime %d times, want none", runtimeCalls.Load())
	}
	h.SetContainerGate(trustGateOf(trust.Full))
	_ = h.redeployAll(httptest.NewRequest("POST", "/", nil), "proj_p1")
	if runtimeCalls.Load() == 0 {
		t.Error("a verified owner's functions were not redeployed")
	}
}

func TestRedeployFunction_NotRunForALimitedOwner(t *testing.T) {
	h, client := suspendedInvokeHandler(t)
	h.SetOrgGuard(nil)
	h.SetContainerGate(trustGateOf(trust.Limited))
	fn, _ := h.store.Get("proj_p1", "hello")
	if err := h.redeployFunction(context.Background(), client, fn); !errors.Is(err, trust.ErrVerificationRequired) {
		t.Fatalf("redeployFunction err = %v, want ErrVerificationRequired", err)
	}
}

func TestReplayDeploys_RefusedForALimitedOwner(t *testing.T) {
	h, _ := suspendedInvokeHandler(t)
	h.SetOrgGuard(nil)
	h.SetContainerGate(trustGateOf(trust.Limited))
	if _, err := h.ReplayDeploys(context.Background(), "proj_p1"); !errors.Is(err, trust.ErrVerificationRequired) {
		t.Fatalf("ReplayDeploys err = %v, want ErrVerificationRequired", err)
	}
}

func TestCreateFunction_RefusedForALimitedOwner(t *testing.T) {
	var runtimeCalls atomic.Int32
	runtime := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { runtimeCalls.Add(1) }))
	t.Cleanup(runtime.Close)
	h, _ := newFunctionHandlerFor(t, runtime.URL)
	h.SetContainerGate(trustGateOf(trust.Limited))
	w := deployFunction(functionCreateRouter(h), "export default () => new Response('v1')")
	if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), `"code":"trust_required"`) {
		t.Fatalf("deploy: %d %s", w.Code, w.Body.String())
	}
	if runtimeCalls.Load() != 0 {
		t.Errorf("the runtime was called %d times", runtimeCalls.Load())
	}
}
