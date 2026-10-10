package dockerapps

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/volume"

	"github.com/excalibase/provisioning-poc/internal/k8s"
)

// ApplyAppWorkload starts the deploy's containers beside the ones serving, as
// a rolling update with one surge does; the route moves only once they are
// ready (WaitForAppRollout). With a disk the old ones stop first, as Recreate.
func (r *Runtime) ApplyAppWorkload(ctx context.Context, namespace string, workload *k8s.AppWorkload) error {
	spec, err := specFromWorkload(workload)
	if err != nil {
		return err
	}
	if err := r.requireProject(namespace, spec.Project); err != nil {
		return err
	}
	if err := r.ensureProjectNetwork(ctx, spec.Project, namespace, spec.Port != 0); err != nil {
		return err
	}
	if err := r.recordPending(spec); err != nil {
		return err
	}
	if spec.Replicas > 0 {
		if err := r.pullImage(ctx, spec.Image, spec.Pull); err != nil {
			return err
		}
	}
	existing, err := r.listApps(ctx, labelApp, spec.AppID)
	if err != nil {
		return err
	}
	// A retried apply starts this deploy over; Recreate also clears every other deploy.
	stale := where(existing, func(c container.Summary) bool { return spec.Recreate || c.Labels[labelDeploy] == spec.DeployID })
	if err := r.retire(ctx, stale); err != nil {
		return err
	}
	ids := make([]string, 0, spec.Replicas)
	for replica := range spec.Replicas {
		id, err := r.createAppContainer(ctx, spec, replica)
		if err != nil {
			return err
		}
		ids = append(ids, id)
	}
	return r.start(ctx, ids)
}

// requireProject refuses to run one project's app in another's scope.
func (r *Runtime) requireProject(namespace, project string) error {
	owner, err := r.opts.Projects.ProjectOf(namespace)
	if err != nil {
		return fmt.Errorf("find the project of %s: %w", namespace, err)
	}
	if owner != project {
		return fmt.Errorf("the workload of project %s cannot run in the scope of project %s", project, owner)
	}
	return nil
}

func (r *Runtime) recordPending(spec containerSpec) error {
	host := ""
	if spec.Port != 0 {
		var err error
		if host, err = r.opts.Route.Hostname(spec.AppName, spec.Project); err != nil {
			return err
		}
	}
	return r.updateRecord(spec.Project, spec.AppID, func(record *appRecord) error {
		record.AppName, record.Port, record.Host, record.Withdrawn = spec.AppName, spec.Port, host, false
		record.Pending = &pendingDeploy{Deploy: spec.DeployID, Replicas: spec.Replicas}
		record.Deploy = spec.DeployID
		return nil
	})
}

// WaitForAppRollout succeeds once every container of the deploy has passed
// its readiness check for MinReady; it then routes to them and retires the
// containers earlier deploys of the same name left.
func (r *Runtime) WaitForAppRollout(ctx context.Context, namespace, name, deployID string, timeout time.Duration) error {
	project, err := r.opts.Projects.ProjectOf(namespace)
	if err != nil {
		return err
	}
	waitCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	watch := newReadiness(r)
	for {
		record, ids, err := r.deployContainers(waitCtx, project, deployID)
		if err != nil {
			return err
		}
		if record != nil && len(ids) >= record.Pending.Replicas {
			ready, err := watch.check(waitCtx, ids)
			if err != nil {
				return fmt.Errorf("%w: %s %v", k8s.ErrAppRollout, name, err)
			}
			if ready {
				return r.finishRollout(ctx, record, deployID)
			}
		}
		select {
		case <-waitCtx.Done():
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if record == nil {
				return fmt.Errorf("%w: %s never ran this deploy's workload within %s", k8s.ErrAppRollout, name, timeout)
			}
			return fmt.Errorf("%w: %s did not become ready within %s", k8s.ErrAppRollout, name, timeout)
		case <-time.After(r.opts.PollInterval):
		}
	}
}

// deployContainers finds the app rolling deployID out and its containers.
func (r *Runtime) deployContainers(ctx context.Context, project, deployID string) (*appRecord, []string, error) {
	records, err := r.projectRecords(project)
	if err != nil {
		return nil, nil, err
	}
	for _, record := range records {
		if record.Pending == nil || record.Pending.Deploy != deployID {
			continue
		}
		list, err := r.listApps(ctx, labelDeploy, deployID, labelApp, record.AppID)
		if err != nil {
			return nil, nil, err
		}
		ids := make([]string, 0, len(list))
		for _, summary := range list {
			ids = append(ids, summary.ID)
		}
		return record, ids, nil
	}
	return nil, nil, nil
}

// finishRollout moves the route only while this deploy is still the app's
// newest; a newer deploy applied meanwhile owns the route now.
func (r *Runtime) finishRollout(ctx context.Context, rolled *appRecord, deployID string) error {
	list, err := r.listApps(ctx, labelApp, rolled.AppID)
	if err != nil {
		return err
	}
	current := where(list, func(c container.Summary) bool { return c.Labels[labelDeploy] == deployID })
	names := make([]string, 0, len(current))
	for _, summary := range current {
		names = append(names, nameOf(summary))
	}
	superseded := false
	err = r.updateRecord(rolled.Project, rolled.AppID, func(record *appRecord) error {
		if record.Pending == nil || record.Pending.Deploy != deployID {
			superseded = true
			return nil
		}
		record.Serving, record.Pending = names, nil
		return nil
	})
	if err != nil || superseded {
		return err
	}
	earlier := where(list, func(c container.Summary) bool {
		return c.Labels[labelDeploy] != deployID && c.Labels[labelAppName] == rolled.AppName
	})
	return r.retire(ctx, earlier)
}

// AppAvailableReplicas counts the app's containers of any deploy that pass their readiness check now.
func (r *Runtime) AppAvailableReplicas(ctx context.Context, namespace, name string) (int32, error) {
	project, err := r.opts.Projects.ProjectOf(namespace)
	if err != nil {
		return 0, err
	}
	list, err := r.listApps(ctx, labelProject, project, labelAppName, appObjectAppName(name))
	if err != nil {
		return 0, err
	}
	var available int32
	for _, summary := range list {
		probe, ok := probeOf(summary.Labels)
		if summary.State == container.StateRunning && ok && r.probeReady(ctx, summary.ID, probe) {
			available++
		}
	}
	return available, nil
}

// PruneAppWorkload removes what the app still runs under any name but keepName.
func (r *Runtime) PruneAppWorkload(ctx context.Context, _ string, appID, keepName string, _ time.Duration) error {
	list, err := r.listApps(ctx, labelApp, appID)
	if err != nil {
		return err
	}
	return r.retire(ctx, where(list, func(c container.Summary) bool { return c.Labels[labelAppName] != keepName }))
}

// DeleteAppWorkload takes the route away first, then the containers, then the
// disks once nothing can write to them; the project's network goes with its last app.
func (r *Runtime) DeleteAppWorkload(ctx context.Context, namespace, appID string, _ time.Duration) error {
	project, err := r.opts.Projects.ProjectOf(namespace)
	if err != nil {
		return err
	}
	if err := r.removeRecord(project, appID); err != nil {
		return err
	}
	list, err := r.listApps(ctx, labelApp, appID)
	if err != nil {
		return err
	}
	if err := r.retire(ctx, list); err != nil {
		return err
	}
	if err := r.removeVolumes(ctx, labelApp, appID); err != nil {
		return err
	}
	return r.removeNetworkIfUnused(ctx, project, namespace)
}

func (r *Runtime) removeVolumes(ctx context.Context, pairs ...string) error {
	volumes, err := r.engine.VolumeList(ctx, volume.ListOptions{Filters: labelFilter(pairs...)})
	if err != nil {
		return fmt.Errorf("list app disks: %w", err)
	}
	var errs []error
	for _, disk := range volumes.Volumes {
		if !strings.HasPrefix(disk.Name, r.opts.VolumePrefix) {
			continue
		}
		if err := r.engine.VolumeRemove(ctx, disk.Name, false); err != nil {
			errs = append(errs, fmt.Errorf("remove disk %s: %w", disk.Name, err))
		}
	}
	return errors.Join(errs...)
}

func (r *Runtime) removeNetworkIfUnused(ctx context.Context, project, namespace string) error {
	remaining, err := r.listApps(ctx, labelProject, project)
	if err != nil {
		return err
	}
	records, err := r.projectRecords(project)
	if err != nil || len(remaining) > 0 || len(records) > 0 {
		return err
	}
	return r.removeProjectNetwork(ctx, project, namespace)
}
