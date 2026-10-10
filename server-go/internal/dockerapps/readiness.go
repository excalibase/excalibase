package dockerapps

import (
	"context"
	"fmt"
	"time"

	"github.com/containerd/errdefs"
)

// readiness follows a set of containers the way the kubelet's readiness probe
// and a Deployment's minReadySeconds do: each must pass its probe and keep
// passing for MinReady without restarting.
type readiness struct {
	runtime  *Runtime
	since    map[string]time.Time
	restarts map[string]int
}

func newReadiness(r *Runtime) *readiness {
	return &readiness{runtime: r, since: map[string]time.Time{}, restarts: map[string]int{}}
}

// check is true once every container is ready; a container that keeps
// crashing, or is removed under the watch, fails it.
func (w *readiness) check(ctx context.Context, ids []string) (bool, error) {
	all := true
	for _, id := range ids {
		ready, err := w.ready(ctx, id)
		if err != nil {
			return false, err
		}
		all = all && ready
	}
	return all, nil
}

func (w *readiness) ready(ctx context.Context, id string) (bool, error) {
	inspect, err := w.runtime.engine.ContainerInspect(ctx, id)
	if errdefs.IsNotFound(err) {
		return false, fmt.Errorf("container %s was removed during the rollout", id)
	}
	if err != nil || inspect.ContainerJSONBase == nil || inspect.State == nil || inspect.Config == nil {
		return false, nil
	}
	running := inspect.State.Running && !inspect.State.Restarting
	if !running && inspect.RestartCount >= w.runtime.opts.CrashRestarts {
		return false, fmt.Errorf("container %s keeps exiting (restarted %d times, last exit code %d)",
			inspect.Name, inspect.RestartCount, inspect.State.ExitCode)
	}
	probe, ok := probeOf(inspect.Config.Labels)
	if !ok {
		return false, fmt.Errorf("container %s has no readiness check", inspect.Name)
	}
	if !running || inspect.RestartCount != w.restarts[id] || !w.runtime.probeReady(ctx, id, probe) {
		w.restarts[id] = inspect.RestartCount
		delete(w.since, id)
		return false, nil
	}
	first, seen := w.since[id]
	if !seen {
		w.since[id] = time.Now()
		return w.runtime.opts.MinReady == 0, nil
	}
	return time.Since(first) >= w.runtime.opts.MinReady, nil
}
