package handler

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/excalibase/provisioning-poc/internal/edgefn"
	"github.com/excalibase/provisioning-poc/internal/k8s"
)

// EXC-569: a secret or allowlist change can replace the project's runtime
// pod. Calls in that window wait for the new pod instead of failing, and a
// caller is told the runtime is restarting, never where it lives.

// runtimeWait bounds how long a call waits on the project's runtime.
type runtimeWait struct {
	// coldStart covers creating the runtime: image pull and first start.
	coldStart time.Duration
	// rollout covers replacing the pod of a runtime that already serves.
	rollout time.Duration
	poll    time.Duration
	// retry is how long one runtime call waits for a refused connection.
	retry time.Duration
}

func defaultRuntimeWait() runtimeWait {
	return runtimeWait{
		coldStart: 120 * time.Second,
		rollout:   edgefn.DefaultRuntimeRetryBudget,
		poll:      time.Second,
		retry:     edgefn.DefaultRuntimeRetryBudget,
	}
}

const (
	msgRuntimeRestarting   = "the function runtime is restarting; try again in a few seconds"
	msgRuntimeUnavailable  = "the function runtime is unavailable"
	msgRuntimeDidNotAnswer = "the function runtime did not answer"
	runtimeRetryAfter      = "5"
	runtimeStatusRestart   = "restarting"
)

// errRuntimeNotRolledOut: the runtime did not finish rolling out in time.
var errRuntimeNotRolledOut = errors.New("function runtime did not finish rolling out")

// waitForRollout polls until the runtime runs its current spec on an
// available pod, or the budget runs out.
func (h *FunctionHandler) waitForRollout(ctx context.Context, namespace string, budget time.Duration) error {
	if h.k8sClient == nil {
		return nil
	}
	deadline := time.Now().Add(budget)
	for {
		state, err := h.k8sClient.DenoRuntimeRollout(ctx, namespace)
		if err == nil && state == k8s.DenoRuntimeReady {
			return nil
		}
		if err != nil {
			log.Printf("WARN: read function runtime rollout in %s: %v", namespace, err)
		}
		if time.Now().Add(h.runtimeWait.poll).After(deadline) {
			return fmt.Errorf("%w: %s still %v after %s", errRuntimeNotRolledOut, namespace, state, budget)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(h.runtimeWait.poll):
		}
	}
}

// runtimeRestarting reports whether the project's runtime is replacing its
// pod right now. Only a per-project runtime that exists can be.
func (h *FunctionHandler) runtimeRestarting(ctx context.Context, projectID string) bool {
	if h.k8sClient == nil {
		return false
	}
	namespace := h.namespaceFor(projectID)
	if namespace == "" {
		return false
	}
	state, err := h.k8sClient.DenoRuntimeRollout(ctx, namespace)
	if err != nil {
		log.Printf("WARN: read function runtime rollout in %s: %v", namespace, err)
		return false
	}
	return state == k8s.DenoRuntimeRollingOut
}

// writeRuntimeFailure answers a call the runtime could not take for a
// reason of the runtime's own: restarting (503, retry), or the connection
// failing. It reports false for anything else, which the caller answers.
// The detail, with the runtime's address, goes to the log only.
func writeRuntimeFailure(w http.ResponseWriter, action string, err error) bool {
	switch {
	case errors.Is(err, ErrProjectDeleting):
		httpError(w, "project is being deleted", http.StatusConflict)
	case errors.Is(err, ErrProjectRestoring):
		httpError(w, "project is being restored", http.StatusConflict)
	case errors.Is(err, edgefn.ErrRuntimeUnreachable), errors.Is(err, errRuntimeNotRolledOut):
		log.Printf("ERROR: %s: %v", action, err)
		w.Header().Set("Retry-After", runtimeRetryAfter)
		httpError(w, msgRuntimeRestarting, http.StatusServiceUnavailable)
	case errors.Is(err, edgefn.ErrRuntimeTransport):
		log.Printf("ERROR: %s: %v", action, err)
		httpError(w, msgRuntimeDidNotAnswer, http.StatusBadGateway)
	default:
		return false
	}
	return true
}

// writeRuntimeUnavailable answers a runtime that could not be resolved.
func writeRuntimeUnavailable(w http.ResponseWriter, action string, err error) {
	if writeRuntimeFailure(w, action, err) {
		return
	}
	log.Printf("ERROR: %s: %v", action, err)
	httpError(w, msgRuntimeUnavailable, http.StatusServiceUnavailable)
}

// invokeDeployed invokes fn and, when the runtime no longer holds it (a
// restarted pod before the periodic replay reaches it), deploys it again
// from the store and invokes once more.
func (h *FunctionHandler) invokeDeployed(ctx context.Context, client *edgefn.RuntimeClient, fn *edgefn.Function, req edgefn.InvokeRequest) (*edgefn.InvokeResponse, error) {
	resp, err := client.Invoke(ctx, fn.RuntimeID(), req)
	if !errors.Is(err, edgefn.ErrFunctionNotDeployed) {
		return resp, err
	}
	log.Printf("function %s is not in its runtime; deploying it again", fn.RuntimeID())
	if err := h.redeployFunction(ctx, client, fn); err != nil {
		return nil, fmt.Errorf("redeploy %s: %w", fn.RuntimeID(), err)
	}
	return client.Invoke(ctx, fn.RuntimeID(), req)
}

// redeployFunction sends fn to the runtime as replay does: stored source,
// the project's secrets and its egress.
func (h *FunctionHandler) redeployFunction(ctx context.Context, client *edgefn.RuntimeClient, fn *edgefn.Function) error {
	env, err := h.deployEnv(ctx, fn.ProjectID)
	if err != nil {
		return err
	}
	code, err := fn.BundleWith(h.sharedFilesFor(fn.ProjectID))
	if err != nil {
		return fmt.Errorf("bundle: %w", err)
	}
	return client.Deploy(ctx, deployRequestFor(fn, code, env, h.workerEgressHosts(fn.ProjectID)))
}
