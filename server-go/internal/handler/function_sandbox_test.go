package handler

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/k8s"
)

// A cluster that cannot offer the gVisor class fails the feature, not the process:
// deploy and invoke answer 503 with the reason, and nothing is provisioned.
func TestFunctionDeploy_WithoutAGVisorClassAnswers503(t *testing.T) {
	forbidden := errors.New("runtimeclasses is forbidden")
	cases := map[string]func(*k8s.MockClient){
		"class missing": func(*k8s.MockClient) {},
		"read forbidden": func(m *k8s.MockClient) {
			m.RuntimeClassError = forbidden
		},
		"handler is runc": func(m *k8s.MockClient) {
			m.RuntimeClasses["gvisor"] = true
			m.Placement = k8s.RuntimePlacement{Handler: "runc"}
		},
	}
	for name, arrange := range cases {
		t.Run(name, func(t *testing.T) {
			f := setupRestartHandler(t)
			f.handler.SetRuntimeClass("gvisor")
			arrange(f.k8s)
			w := doJSON(f.router, http.MethodPost, testRestartBase+"/", restartFnBody("hello"))
			if w.Code != http.StatusServiceUnavailable || !strings.Contains(w.Body.String(), "sandbox") {
				t.Fatalf("got %d %s, want a 503 naming the sandbox", w.Code, w.Body.String())
			}
			if strings.Contains(w.Body.String(), forbidden.Error()) {
				t.Errorf("the cluster's error text reached the client: %s", w.Body.String())
			}
			if len(f.k8s.DenoRuntimes) != 0 {
				t.Errorf("a runtime was created without a sandbox: %v", f.k8s.DenoRuntimes)
			}
		})
	}
}

func TestFunctionDeploy_WithGVisorClassRuns(t *testing.T) {
	f := setupRestartHandler(t)
	f.handler.SetRuntimeClass("gvisor")
	f.k8s.RuntimeClasses["gvisor"] = true
	f.k8s.Placement = k8s.RuntimePlacement{Handler: "runsc"}
	f.runtime.start(t)
	if w := doJSON(f.router, http.MethodPost, testRestartBase+"/", restartFnBody("hello")); w.Code != http.StatusCreated {
		t.Fatalf("deploy: %d %s", w.Code, w.Body.String())
	}
}

// A project whose runtime was cached before the class went bad is refused too.
func TestFunctionInvoke_RechecksTheSandbox(t *testing.T) {
	f := setupRestartHandler(t)
	f.handler.SetRuntimeClass("gvisor")
	f.k8s.RuntimeClasses["gvisor"] = true
	f.k8s.Placement = k8s.RuntimePlacement{Handler: "runsc"}
	f.runtime.start(t)
	if w := doJSON(f.router, http.MethodPost, testRestartBase+"/", restartFnBody("hello")); w.Code != http.StatusCreated {
		t.Fatalf("deploy: %d %s", w.Code, w.Body.String())
	}
	f.handler.sandbox.expire()
	f.k8s.Placement = k8s.RuntimePlacement{Handler: "runc"}
	w := doJSON(f.router, http.MethodPost, testRestartBase+"/hello/invoke", map[string]any{})
	if w.Code != http.StatusServiceUnavailable || !strings.Contains(w.Body.String(), "sandbox") {
		t.Fatalf("got %d %s, want a 503 naming the sandbox", w.Code, w.Body.String())
	}
}

// Replay pushes code into the runtime the cluster has, so it first brings that
// runtime under the sandbox class.
func TestFunctionHandler_ReplayDeploys_EnsuresTheRuntime(t *testing.T) {
	rt := newRestartableRuntime(t, "boot-1")
	h, store, fake := newReplayHandler(t, rt)
	saveReplayFn(t, store, "alpha", "")
	if _, err := h.ReplayDeploys(context.Background(), testReplayProject); err != nil {
		t.Fatal(err)
	}
	if fake.ensured != 1 {
		t.Fatalf("EnsureDenoRuntime calls = %d, want 1", fake.ensured)
	}
}

// At startup every existing runtime is reconciled, so none stays outside the
// sandbox until its next cold invoke; a project without a runtime gets none.
func TestFunctionHandler_ReconcileRuntimes(t *testing.T) {
	rt := newRestartableRuntime(t, "boot-1")
	h, store, fake := newReplayHandler(t, rt)
	saveReplayFn(t, store, "alpha", "")
	fake.hasRuntime = true
	h.ReconcileRuntimes(context.Background())
	if fake.ensured != 1 {
		t.Fatalf("reconcile ensured %d runtimes, want 1", fake.ensured)
	}
	fake.ensured, fake.hasRuntime = 0, false
	h.ReconcileRuntimes(context.Background())
	if fake.ensured != 0 {
		t.Fatalf("a project with no runtime must not get one at startup, got %d", fake.ensured)
	}
}
