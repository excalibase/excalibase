package dockerapps

import (
	"context"
	"fmt"
	"time"

	"github.com/docker/docker/api/types/container"

	"github.com/excalibase/provisioning-poc/internal/k8s"
)

const reasonOOMKilled, reasonError = "OOMKilled", "Error"

// AppRestarts is what the engine knows of the app's containers' restarts
// (EXC-651). The engine clears the stop reason when it starts a container
// again, so a running container reports its count and last stop time only.
func (r *Runtime) AppRestarts(ctx context.Context, namespace, appID string) (k8s.AppRestarts, error) {
	project, err := r.opts.Projects.ProjectOf(namespace)
	if err != nil {
		return k8s.AppRestarts{}, err
	}
	list, err := r.listApps(ctx, labelProject, project, labelApp, appID)
	if err != nil {
		return k8s.AppRestarts{}, err
	}
	var restarts k8s.AppRestarts
	// A stopped container was stopped by the platform (a pause), not a crash.
	for _, summary := range where(list, isUp) {
		inspect, err := r.engine.ContainerInspect(ctx, summary.ID)
		if err != nil {
			return k8s.AppRestarts{}, fmt.Errorf("read %s: %w", nameOf(summary), err)
		}
		if inspect.ContainerJSONBase == nil || inspect.State == nil {
			continue
		}
		restarts.Count += int32(inspect.RestartCount)
		noteStop(&restarts, inspect.State)
	}
	return restarts, nil
}

// noteStop keeps the newest stop the engine recorded.
func noteStop(restarts *k8s.AppRestarts, state *container.State) {
	at, err := time.Parse(time.RFC3339Nano, state.FinishedAt)
	if err != nil || at.IsZero() || at.Year() <= 1 || (restarts.LastAt != nil && !at.After(*restarts.LastAt)) {
		return
	}
	restarts.LastAt, restarts.LastReason, restarts.LastExitCode = &at, "", nil
	if state.Running && !state.Restarting {
		return
	}
	exitCode := int32(state.ExitCode)
	restarts.LastExitCode = &exitCode
	switch {
	case state.OOMKilled:
		restarts.LastReason = reasonOOMKilled
	case state.ExitCode != 0:
		restarts.LastReason = reasonError
	}
}
