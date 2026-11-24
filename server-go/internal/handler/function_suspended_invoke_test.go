package handler

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/edgefn"
	"github.com/excalibase/provisioning-poc/internal/k8s"
	"github.com/excalibase/provisioning-poc/internal/orgguard"
	"github.com/go-chi/chi/v5"
)

// suspendedInvokeHandler deploys an unauthenticated function into a mock
// runtime for a project whose org owner is then suspended.
func suspendedInvokeHandler(t *testing.T) (*FunctionHandler, *edgefn.RuntimeClient) {
	t.Helper()
	store := edgefn.NewFunctionStore(t.TempDir())
	runtime, _ := mockFnRuntime(t)
	client := edgefn.NewRuntimeClient(runtime.URL, "")
	instStore := &inMemoryInstanceStore{insts: map[string]*domain.DatabaseInstance{
		"proj_p1": {ProjectID: "proj_p1", OrgID: "default", Namespace: "ns-1", Status: "ACTIVE"},
	}}
	h := NewFunctionHandler(store, edgefn.NewSecretsStore(newFakeVault()), client, instStore, nil, testAPIBase)
	verify := false
	fn := &edgefn.Function{ProjectID: "proj_p1", ID: "hello", Name: "Hello", Active: true, VerifyJwt: &verify,
		Files: []edgefn.File{{Path: testIndexTS, Content: testDefaultHandler}}}
	if err := store.Save(fn); err != nil {
		t.Fatal(err)
	}
	bundled, _ := fn.Bundle()
	if err := client.Deploy(context.Background(), edgefn.DeployRequest{ID: fn.RuntimeID(), Code: bundled}); err != nil {
		t.Fatal(err)
	}
	h.SetOrgGuard(suspendedOwnerOf(instStore, "default"))
	return h, client
}

// Single host: the shared runtime keeps serving a suspended org's public
// function URL; nothing on the invoke path consults the guard.
func TestPublicInvoke_SharedRuntimeRefusesASuspendedOrg(t *testing.T) {
	h, _ := suspendedInvokeHandler(t)
	r := chi.NewRouter()
	r.HandleFunc(testPublicInvokeRoute, h.PublicInvoke)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/functions/v1/proj_p1/hello", nil))
	if w.Code != http.StatusForbidden {
		t.Fatalf("public invoke of a suspended org's function: %d %s, want 403", w.Code, w.Body.String())
	}
}

// k8s: a runtime client cached before the suspension skips the guard, so a
// warm project's functions stay invocable until the replica restarts.
func TestRuntimeClientFor_WarmClientRefusesASuspendedOrg(t *testing.T) {
	h, client := suspendedInvokeHandler(t)
	h.SetK8sClient(k8s.NewMockClient(), "deno:test", "secret")
	h.clientMu.Lock()
	h.clients["proj_p1"] = client
	h.clientMu.Unlock()

	if _, err := h.runtimeClientFor(context.Background(), "proj_p1"); !errors.Is(err, orgguard.ErrOrgSuspended) {
		t.Fatalf("runtimeClientFor with a warm client = %v, want ErrOrgSuspended", err)
	}
}

// The function scheduler dispatches through the same warm client: cron
// tasks of a suspended org keep firing.
func TestSchedulerInvoker_WarmClientRefusesASuspendedOrg(t *testing.T) {
	h, client := suspendedInvokeHandler(t)
	h.SetK8sClient(k8s.NewMockClient(), "deno:test", "secret")
	h.clientMu.Lock()
	h.clients["proj_p1"] = client
	h.clientMu.Unlock()

	if err := h.SchedulerInvoker().Invoke(context.Background(), "proj_p1", "hello", "default", nil); err == nil {
		t.Fatal("a scheduled task of a suspended org ran")
	}
}

// The cold-start replayer redeploys every stored function into a runtime that
// lost them (pod restart, node drain) and ensures the runtime first, with no
// suspension check: a suspended org's runtime is re-armed by the platform.
func TestReplayDeploys_RefusedForASuspendedOrg(t *testing.T) {
	h, _ := suspendedInvokeHandler(t)
	kube := k8s.NewMockClient()
	h.SetK8sClient(kube, "deno:test", "secret")

	deploys, err := h.ReplayDeploys(context.Background(), "proj_p1")
	if !errors.Is(err, orgguard.ErrOrgSuspended) {
		t.Fatalf("ReplayDeploys = %d deploys, err %v; want ErrOrgSuspended", len(deploys), err)
	}
	for _, call := range kube.Calls {
		if len(call) >= len("EnsureDenoRuntime") && call[:len("EnsureDenoRuntime")] == "EnsureDenoRuntime" {
			t.Errorf("replay ensured the suspended org's runtime: %v", kube.Calls)
		}
	}
}
