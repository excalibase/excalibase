package handler

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

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

func TestSandboxGate_CachesUntilExpired(t *testing.T) {
	mock := k8s.NewMockClient()
	mock.RuntimeClasses["gvisor"] = true
	mock.Placement = k8s.RuntimePlacement{Handler: "runsc"}
	gate := &sandboxGate{ttl: time.Hour}
	for range 3 {
		if err := gate.check(context.Background(), mock, "gvisor"); err != nil {
			t.Fatal(err)
		}
	}
	if reads := countCalls(mock, "RuntimeClassExists:gvisor"); reads != 1 {
		t.Fatalf("%d cluster reads for three checks, want 1", reads)
	}
	gate.expire()
	_ = gate.check(context.Background(), mock, "gvisor")
	if reads := countCalls(mock, "RuntimeClassExists:gvisor"); reads != 2 {
		t.Fatalf("%d cluster reads after expiry, want 2", reads)
	}
}

func countCalls(mock *k8s.MockClient, call string) int {
	n := 0
	for _, c := range mock.Calls {
		if c == call {
			n++
		}
	}
	return n
}

func TestReadSandbox_PlacementReadFailureIsUnavailable(t *testing.T) {
	mock := k8s.NewMockClient()
	mock.RuntimeClasses["gvisor"] = true
	mock.PlacementErr = errors.New("forbidden")
	if err := readSandbox(context.Background(), mock, "gvisor"); !errors.Is(err, ErrSandboxUnavailable) {
		t.Fatalf("got %v, want ErrSandboxUnavailable", err)
	}
}

func TestRequireSandbox_OffWithoutAClassOrCluster(t *testing.T) {
	h := &FunctionHandler{sandbox: &sandboxGate{ttl: time.Hour}}
	if err := h.requireSandbox(context.Background()); err != nil {
		t.Fatalf("no cluster: %v", err)
	}
	h.k8sClient = k8s.NewMockClient()
	if err := h.requireSandbox(context.Background()); err != nil {
		t.Fatalf("no class named: %v", err)
	}
}

func TestEnsureRuntime_RefusedWithoutSandboxOrOnEnsureFailure(t *testing.T) {
	f := setupRestartHandler(t)
	f.handler.SetRuntimeClass("gvisor")
	if err := f.handler.ensureRuntime(context.Background(), testRestartProject, testRestartNS, time.Second); !errors.Is(err, ErrSandboxUnavailable) {
		t.Fatalf("got %v, want ErrSandboxUnavailable", err)
	}
	f.k8s.RuntimeClasses["gvisor"] = true
	f.k8s.Placement = k8s.RuntimePlacement{Handler: "runsc"}
	f.k8s.EnsureDenoError = errors.New("boom")
	if err := f.handler.ensureRuntime(context.Background(), testRestartProject, testRestartNS, time.Second); err == nil {
		t.Fatal("an EnsureDenoRuntime failure must surface")
	}
}

func TestReconcileRuntimes_SkipsWhenTheSandboxIsUnavailable(t *testing.T) {
	rt := newRestartableRuntime(t, "boot-1")
	h, store, fake := newReplayHandler(t, rt)
	saveReplayFn(t, store, "alpha", "")
	fake.hasRuntime = true
	h.SetRuntimeClass("gvisor")
	h.k8sClient = &sandboxlessK8s{replayK8s: fake}
	h.ReconcileRuntimes(context.Background())
	if fake.ensured != 0 {
		t.Fatalf("reconciled %d runtimes without a sandbox", fake.ensured)
	}
}

type sandboxlessK8s struct{ *replayK8s }

func (s *sandboxlessK8s) RuntimeClassExists(context.Context, string) (bool, error) { return false, nil }
