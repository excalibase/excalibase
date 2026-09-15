package handler

import (
	"context"
	"fmt"
	"log"

	"github.com/excalibase/provisioning-poc/internal/edgefn"
)

// NewReplayer wires an edgefn.Replayer to this handler: projects come from the
// function store, deploy sets are rebuilt from stored source (bundle + secrets
// + schema preamble), and runtimes are resolved without provisioning (EXC-337).
// Errors when the store cannot enumerate projects.
func (h *FunctionHandler) NewReplayer(cfg edgefn.ReplayConfig) (*edgefn.Replayer, error) {
	lister, ok := h.store.(edgefn.ProjectLister)
	if !ok {
		return nil, fmt.Errorf("function store %T cannot list projects", h.store)
	}
	cfg.Projects = lister.ProjectIDs
	cfg.Runtime = h.ReplayRuntimeFor
	cfg.Deploys = h.ReplayDeploys
	return edgefn.NewReplayer(cfg), nil
}

// ReplayDeploys rebuilds the deploy request for every function of the project
// from the store — exactly what Create sent the first time.
func (h *FunctionHandler) ReplayDeploys(ctx context.Context, projectID string) ([]edgefn.DeployRequest, error) {
	list, err := h.store.List(projectID)
	if err != nil {
		return nil, fmt.Errorf("list functions: %w", err)
	}
	if len(list) == 0 {
		return []edgefn.DeployRequest{}, nil
	}
	env, err := h.deployEnv(ctx, projectID)
	if err != nil {
		return nil, err
	}
	shared := h.sharedFilesFor(projectID)
	allowedHosts := h.effectiveEgressHosts(projectID)
	out := make([]edgefn.DeployRequest, 0, len(list))
	for _, fn := range list {
		code, err := fn.BundleWith(shared)
		if err != nil {
			log.Printf("WARN: replay bundle %s/%s: %v", projectID, fn.ID, err)
			continue
		}
		out = append(out, deployRequestFor(fn, code, env, allowedHosts))
	}
	return out, nil
}

// ReplayRuntimeFor resolves the project's runtime client WITHOUT creating the
// runtime: a cached client, the shared client in single-runtime mode, or a
// client for the project's namespace service. A project with no namespace
// errors; an absent runtime surfaces as an unreachable Status.
func (h *FunctionHandler) ReplayRuntimeFor(_ context.Context, projectID string) (edgefn.ReplayRuntime, error) {
	if client, ok := h.cachedClient(projectID); ok {
		return client, nil
	}
	if h.k8sClient == nil {
		return nil, fmt.Errorf("no runtime client available")
	}
	namespace := h.namespaceFor(projectID)
	if namespace == "" {
		return nil, fmt.Errorf("no namespace for project %s", projectID)
	}
	return h.newProjectClient(projectID, namespace), nil
}

// cachedClient returns the project's cached client, or the shared client in
// single-runtime mode. The instance-store lookup stays outside the lock.
func (h *FunctionHandler) cachedClient(projectID string) (*edgefn.RuntimeClient, bool) {
	h.clientMu.Lock()
	defer h.clientMu.Unlock()
	if client, ok := h.clients[projectID]; ok {
		return client, true
	}
	if shared, ok := h.clients[sharedClientKey]; ok && h.k8sClient == nil {
		return shared, true
	}
	return nil, false
}
