package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/excalibase/provisioning-poc/internal/edgefn"
	"github.com/excalibase/provisioning-poc/internal/scheduler"
)

// schedulerInvokeTimeout bounds one scheduled dispatch. The runtime stops a
// worker at 30s and the runtime client gives up at 35s, so anything beyond
// this is a runtime that stopped answering rather than a slow function.
const schedulerInvokeTimeout = 40 * time.Second

// RuntimeInvoker dispatches a scheduled task to the project's Deno runtime.
// It is the production implementation of scheduler.Invoker and deliberately
// reuses the handler's own resolution path: the same client cache, the same
// per-project runtime credential, and the same refusal to serve a project a
// teardown or an unconfirmed restore owns.
type RuntimeInvoker struct {
	handler *FunctionHandler
	timeout time.Duration
}

// SchedulerInvoker returns the invoker the function scheduler dispatches
// through. It holds the handler, not a copy of its wiring, so a runtime
// client evicted by a project's teardown is re-resolved here too.
func (h *FunctionHandler) SchedulerInvoker() *RuntimeInvoker {
	return &RuntimeInvoker{handler: h, timeout: schedulerInvokeTimeout}
}

// scheduledInvokeBody is the request body shape the runtime reads for a
// v2 export: `{args, txnRefId?, runDepth?}`. A scheduled task is always a
// top-level call, so it carries args alone.
type scheduledInvokeBody struct {
	Args json.RawMessage `json:"args"`
}

// Invoke runs one scheduled task. A project that may not be served is
// reported as scheduler.ErrNotServable so the worker closes the task
// instead of spending its retries; every other failure is returned as-is
// and the worker's retry policy decides.
func (i *RuntimeInvoker) Invoke(
	ctx context.Context,
	projectID, moduleName, exportName string,
	args json.RawMessage,
) error {
	ctx, cancel := context.WithTimeout(ctx, i.timeout)
	defer cancel()

	client, err := i.handler.runtimeClientFor(ctx, projectID)
	if err != nil {
		if errors.Is(err, ErrProjectDeleting) || errors.Is(err, ErrProjectRestoring) {
			return fmt.Errorf("%w: %v", scheduler.ErrNotServable, err)
		}
		return fmt.Errorf("resolve runtime for %s: %w", projectID, err)
	}

	runtimeID := projectID + "__" + moduleName
	body, err := json.Marshal(scheduledInvokeBody{Args: argsOrEmpty(args)})
	if err != nil {
		return fmt.Errorf("marshal args for %s.%s: %w", moduleName, exportName, err)
	}
	req := edgefn.InvokeRequest{
		Method:  "POST",
		URL:     "/invoke/" + runtimeID,
		Headers: map[string]string{"content-type": "application/json"},
		Body:    string(body),
	}
	resp, err := client.Invoke(ctx, runtimeID, req)
	if errors.Is(err, edgefn.ErrFunctionNotDeployed) {
		resp, err = i.invokeStored(ctx, client, projectID, moduleName, req, err)
	}
	if err != nil {
		return fmt.Errorf("invoke %s.%s: %w", moduleName, exportName, err)
	}
	if resp.Status >= 400 {
		return fmt.Errorf("invoke %s.%s: function returned %d", moduleName, exportName, resp.Status)
	}
	return nil
}

// invokeStored deploys a function a restarted runtime lost and invokes it
// again. A function the store no longer holds keeps the runtime's answer.
func (i *RuntimeInvoker) invokeStored(ctx context.Context, client *edgefn.RuntimeClient, projectID, moduleName string, req edgefn.InvokeRequest, notDeployed error) (*edgefn.InvokeResponse, error) {
	if i.handler.store == nil {
		return nil, notDeployed
	}
	fn, err := i.handler.store.Get(projectID, moduleName)
	if err != nil {
		return nil, fmt.Errorf("read function: %w", err)
	}
	if fn == nil {
		return nil, notDeployed
	}
	if err := i.handler.redeployFunction(ctx, client, fn); err != nil {
		return nil, fmt.Errorf("redeploy %s: %w", fn.RuntimeID(), err)
	}
	return client.Invoke(ctx, fn.RuntimeID(), req)
}

// argsOrEmpty keeps the body valid JSON for a task stored without args.
func argsOrEmpty(args json.RawMessage) json.RawMessage {
	if len(args) == 0 {
		return json.RawMessage(`{}`)
	}
	return args
}

// storeRegistry answers the sweep's "did the platform deploy this?" from the
// function store — the same record the invoke routes resolve against.
type storeRegistry struct{ store edgefn.Store }

func (r storeRegistry) HasFunction(projectID, moduleName string) (bool, error) {
	if r.store == nil {
		return false, errors.New("no function store configured")
	}
	fn, err := r.store.Get(projectID, moduleName)
	if err != nil {
		return false, err
	}
	return fn != nil, nil
}

// SchedulerFunctions returns the registry the sweep checks a claimed row's
// module name against. Rows are tenant-written; only a module the platform
// deployed for that project may run.
func (h *FunctionHandler) SchedulerFunctions() scheduler.FunctionChecker {
	return storeRegistry{store: h.store}
}
