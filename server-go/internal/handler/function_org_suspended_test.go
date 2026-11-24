package handler

import (
	"context"
	"errors"
	"github.com/excalibase/provisioning-poc/internal/edgefn"
	"github.com/excalibase/provisioning-poc/internal/k8s"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/orgguard"
	"github.com/excalibase/provisioning-poc/internal/storage"
)

func suspendedOwnerOf(instances storage.InstanceStore, orgs ...string) *orgguard.Guard {
	set := map[string]bool{}
	for _, id := range orgs {
		set[id] = true
	}
	return orgguard.New(instances, func(context.Context) (map[string]bool, error) { return set, nil })
}

func TestCreateFunction_RefusedWhileTheOrgOwnerIsSuspended(t *testing.T) {
	var runtimeCalls atomic.Int32
	runtime := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { runtimeCalls.Add(1) }))
	t.Cleanup(runtime.Close)
	h, instances := newFunctionHandlerFor(t, runtime.URL)
	h.SetOrgGuard(suspendedOwnerOf(instances, "default"))
	router := functionCreateRouter(h)

	w := deployFunction(router, "export default () => new Response('v1')")
	if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), orgguard.Message) {
		t.Fatalf("deploy: %d %s, want 403 with the suspension message", w.Code, w.Body.String())
	}
	if runtimeCalls.Load() != 0 {
		t.Errorf("the runtime was called %d times, want none", runtimeCalls.Load())
	}
	if fn, _ := h.store.Get("proj_p1", "hello"); fn != nil {
		t.Error("a refused deploy kept the function")
	}
}

func TestCreateFunction_AnUnsuspendedOrgAndAPlatformAdminMayDeploy(t *testing.T) {
	var failDeploys atomic.Bool
	runtime, _ := functionRuntimeServer(t, &failDeploys)
	h, instances := newFunctionHandlerFor(t, runtime.URL)
	h.SetOrgGuard(suspendedOwnerOf(instances, "another-org"))
	if w := deployFunction(functionCreateRouter(h), "export default () => new Response('v1')"); w.Code != http.StatusCreated {
		t.Fatalf("unsuspended org: %d %s", w.Code, w.Body.String())
	}

	h.SetOrgGuard(suspendedOwnerOf(instances, "default"))
	router := functionCreateRouter(h)
	w := httptest.NewRecorder()
	body := `{"id":"hello","name":"Hello","files":[{"path":"index.ts","content":"export default () => new Response('v2')"}]}`
	req := httptest.NewRequest("POST", "/api/projects/proj_p1/functions", strings.NewReader(body))
	router.ServeHTTP(w, req.WithContext(orgguard.WithPlatformOverride(req.Context())))
	if w.Code != http.StatusCreated {
		t.Fatalf("platform admin: %d %s", w.Code, w.Body.String())
	}
}

// Starting a project's runtime and redeploying its functions are refused too,
// not only the deploy request.
func TestFunctionRuntime_NotStartedWhileTheOrgOwnerIsSuspended(t *testing.T) {
	var runtimeCalls atomic.Int32
	runtime := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { runtimeCalls.Add(1) }))
	t.Cleanup(runtime.Close)
	h, instances := newFunctionHandlerFor(t, runtime.URL)
	kube := k8s.NewMockClient()
	h.SetK8sClient(kube, "deno:test", "secret")
	h.SetOrgGuard(suspendedOwnerOf(instances, "default"))

	if _, err := h.runtimeClientFor(context.Background(), "proj_p1"); !errors.Is(err, orgguard.ErrOrgSuspended) {
		t.Fatalf("runtimeClientFor err = %v, want ErrOrgSuspended", err)
	}
	for _, call := range kube.Calls {
		if strings.HasPrefix(call, "EnsureDenoRuntime") {
			t.Errorf("the runtime was started: %v", kube.Calls)
		}
	}
}

// On a single host the shared runtime has no per-project start, so the
// redeploy after a secret change must be refused by its own check.
func TestRedeployAll_NotRunWhileTheOrgOwnerIsSuspended(t *testing.T) {
	var runtimeCalls atomic.Int32
	runtime := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { runtimeCalls.Add(1) }))
	t.Cleanup(runtime.Close)
	h, instances := newFunctionHandlerFor(t, runtime.URL)
	h.SetOrgGuard(suspendedOwnerOf(instances, "default"))

	if err := h.store.Save(&edgefn.Function{ID: "hello", Name: "Hello", ProjectID: "proj_p1", Active: true,
		Files: []edgefn.File{{Path: "index.ts", Content: "export default () => new Response('x')"}}}); err != nil {
		t.Fatal(err)
	}
	h.redeployAll(httptest.NewRequest("POST", "/", nil), "proj_p1")
	if runtimeCalls.Load() != 0 {
		t.Errorf("redeployAll called the runtime %d times, want none", runtimeCalls.Load())
	}
}
