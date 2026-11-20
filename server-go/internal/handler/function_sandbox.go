package handler

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/excalibase/provisioning-poc/internal/edgefn"
	"github.com/excalibase/provisioning-poc/internal/k8s"
)

// ErrSandboxUnavailable means the cluster cannot offer the gVisor RuntimeClass
// tenant functions must run under: it is missing, unreadable, or its handler
// is not runsc. Functions fail with it; the control plane keeps running.
var ErrSandboxUnavailable = errors.New("the function sandbox is not available")

const (
	msgSandboxUnavailable = "edge functions are unavailable: the gVisor sandbox is not available on this cluster"
	sandboxCheckTTL       = 30 * time.Second
)

// sandboxGate checks the sandbox class at most once per ttl, so a request does
// not cost a cluster read and a restored class is noticed without a restart.
type sandboxGate struct {
	mu      sync.Mutex
	ttl     time.Duration
	checked time.Time
	err     error
}

func (g *sandboxGate) check(ctx context.Context, kube k8s.KubeClient, class string) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if !g.checked.IsZero() && time.Since(g.checked) < g.ttl {
		return g.err
	}
	g.err = readSandbox(ctx, kube, class)
	g.checked = time.Now()
	if g.err != nil {
		log.Printf("ERROR: %v", g.err)
	}
	return g.err
}

// expire makes the next check read the cluster again.
func (g *sandboxGate) expire() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.checked = time.Time{}
}

func readSandbox(ctx context.Context, kube k8s.KubeClient, class string) error {
	found, err := kube.RuntimeClassExists(ctx, class)
	if err != nil {
		return fmt.Errorf("%w: RuntimeClass %q cannot be read: %v", ErrSandboxUnavailable, class, err)
	}
	if !found {
		return fmt.Errorf("%w: RuntimeClass %q does not exist", ErrSandboxUnavailable, class)
	}
	placement, err := kube.RuntimeClassPlacement(ctx, class)
	if err != nil {
		return fmt.Errorf("%w: RuntimeClass %q cannot be read: %v", ErrSandboxUnavailable, class, err)
	}
	if !k8s.IsGVisorHandler(placement.Handler) {
		return fmt.Errorf("%w: RuntimeClass %q has handler %q, want runsc", ErrSandboxUnavailable, class, placement.Handler)
	}
	return nil
}

// requireSandbox is the gate for every per-project runtime use. It is off until
// a sandbox class is named, which production always does.
func (h *FunctionHandler) requireSandbox(ctx context.Context) error {
	if h.k8sClient == nil || h.runtimeClass == "" {
		return nil
	}
	return h.sandbox.check(ctx, h.k8sClient, h.runtimeClass)
}

// ensureRuntime brings the project's runtime to its wanted spec, sandbox class
// included, and waits for any pod it replaces.
func (h *FunctionHandler) ensureRuntime(ctx context.Context, projectID, namespace string, budget time.Duration) error {
	if err := h.requireSandbox(ctx); err != nil {
		return err
	}
	if err := h.k8sClient.EnsureDenoRuntime(ctx, namespace, h.denoRuntimeSpecFor(projectID)); err != nil {
		return fmt.Errorf("ensure deno runtime: %w", err)
	}
	return h.waitForRollout(ctx, namespace, budget)
}

// ReconcileRuntimes brings every existing function runtime to its wanted spec
// at startup, so a runtime made before the sandbox class was required does not
// stay outside it until its next cold invoke. Projects without a runtime get
// none; failures are logged and the next replay or invoke retries.
func (h *FunctionHandler) ReconcileRuntimes(ctx context.Context) {
	lister, ok := h.store.(edgefn.ProjectLister)
	if !ok || h.k8sClient == nil {
		return
	}
	projects, err := lister.ProjectIDs()
	if err != nil {
		log.Printf("WARN: reconcile function runtimes: list projects: %v", err)
		return
	}
	if err := h.requireSandbox(ctx); err != nil {
		return
	}
	for _, projectID := range projects {
		namespace := h.namespaceFor(projectID)
		if namespace == "" {
			continue
		}
		exists, err := h.k8sClient.GetDeployment(ctx, namespace, "deno-runtime")
		if err != nil || !exists {
			continue
		}
		if err := h.k8sClient.EnsureDenoRuntime(ctx, namespace, h.denoRuntimeSpecFor(projectID)); err != nil {
			log.Printf("WARN: reconcile function runtime %s: %v", projectID, err)
		}
	}
}
