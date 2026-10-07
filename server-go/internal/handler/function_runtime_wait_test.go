package handler

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/edgefn"
	"github.com/excalibase/provisioning-poc/internal/k8s"
)

func TestWriteRuntimeFailure(t *testing.T) {
	internal := "dial tcp 10.43.0.7:8000: connect: connection refused"
	cases := []struct {
		name    string
		err     error
		handled bool
		code    int
		body    string
	}{
		{"deleting", fmt.Errorf("%w: proj_x", ErrProjectDeleting), true, http.StatusConflict, "being deleted"},
		{"restoring", fmt.Errorf("%w: proj_x", ErrProjectRestoring), true, http.StatusConflict, "being restored"},
		{"unreachable", fmt.Errorf("deploy: %w: %s", edgefn.ErrRuntimeUnreachable, internal), true, http.StatusServiceUnavailable, "restarting"},
		{"not rolled out", fmt.Errorf("%w: ns", errRuntimeNotRolledOut), true, http.StatusServiceUnavailable, "restarting"},
		{"broken connection", fmt.Errorf("invoke: %w: %s", edgefn.ErrRuntimeTransport, internal), true, http.StatusBadGateway, "did not answer"},
		{"anything else", errors.New("status 400: syntax error"), false, 0, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			if got := writeRuntimeFailure(w, "call", tc.err); got != tc.handled {
				t.Fatalf("handled = %v, want %v", got, tc.handled)
			}
			if !tc.handled {
				return
			}
			if w.Code != tc.code || !strings.Contains(w.Body.String(), tc.body) {
				t.Fatalf("got %d %s", w.Code, w.Body.String())
			}
			if strings.Contains(w.Body.String(), "10.43.0.7") {
				t.Fatalf("leaked the address: %s", w.Body.String())
			}
		})
	}
}

func TestWriteRuntimeUnavailable_HidesTheCause(t *testing.T) {
	w := httptest.NewRecorder()
	writeRuntimeUnavailable(w, "deploy", errors.New("ensure deno runtime: update deno deployment in proj-ns: forbidden"))
	if w.Code != http.StatusServiceUnavailable || strings.Contains(w.Body.String(), "proj-ns") {
		t.Fatalf("got %d %s", w.Code, w.Body.String())
	}
}

func TestWaitForRollout_StopsWhenTheCallerGivesUp(t *testing.T) {
	f := setupRestartHandler(t)
	f.k8s.DenoRolloutFunc = func(string) (k8s.DenoRolloutState, error) { return k8s.DenoRuntimeAbsent, errors.New("apiserver down") }
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if err := f.handler.waitForRollout(ctx, testRestartNS, time.Minute); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want the caller's deadline", err)
	}
}

func TestWaitForRollout_WithoutKubernetesIsImmediate(t *testing.T) {
	h := NewFunctionHandler(nil, nil, nil, nil, nil, "")
	if err := h.waitForRollout(context.Background(), "ns", time.Minute); err != nil {
		t.Fatal(err)
	}
	if h.runtimeRestarting(context.Background(), "proj") {
		t.Fatal("a shared runtime never reports restarting")
	}
}

func TestRuntimeRestarting_UnknownProjectOrUnreadableRollout(t *testing.T) {
	f := setupRestartHandler(t)
	if f.handler.runtimeRestarting(context.Background(), "proj_unknown") {
		t.Fatal("a project without a namespace is not restarting")
	}
	f.k8s.DenoRolloutFunc = func(string) (k8s.DenoRolloutState, error) { return k8s.DenoRuntimeAbsent, errors.New("apiserver down") }
	if f.handler.runtimeRestarting(context.Background(), testRestartProject) {
		t.Fatal("an unreadable rollout is not reported as restarting")
	}
}

func TestRedeployFunction_BundleFailure(t *testing.T) {
	f := setupRestartHandler(t)
	fn := &edgefn.Function{ID: "broken", ProjectID: testRestartProject,
		Files: []edgefn.File{{Path: testIndexTS, Content: "export default (("}}}
	client := edgefn.NewRuntimeClient(f.runtime.url(), "")
	if err := f.handler.redeployFunction(context.Background(), client, fn); err == nil {
		t.Fatal("a function that does not bundle cannot be redeployed")
	}
}

func TestSchedulerInvokeStored_WithoutAStoreKeepsTheRuntimeAnswer(t *testing.T) {
	h := NewFunctionHandler(nil, nil, nil, nil, nil, "")
	notDeployed := fmt.Errorf("invoke: %w", edgefn.ErrFunctionNotDeployed)
	_, err := h.SchedulerInvoker().invokeStored(context.Background(), nil, "proj_a", "jobs", edgefn.InvokeRequest{}, notDeployed)
	if !errors.Is(err, edgefn.ErrFunctionNotDeployed) {
		t.Fatalf("err = %v", err)
	}
}
