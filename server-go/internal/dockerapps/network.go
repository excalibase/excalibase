package dockerapps

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/containerd/errdefs"
	"github.com/docker/docker/api/types/network"
)

// networkOptions: internal unless the operator gave apps egress, so a project
// network has no route off it at all (ADR 0039).
func (r *Runtime) networkOptions(project string) network.CreateOptions {
	options := network.CreateOptions{
		Driver:   "bridge",
		Internal: !r.opts.Egress,
		Labels:   map[string]string{labelManaged: "true", labelProject: project},
	}
	if r.opts.Egress && r.opts.Isolate {
		options.Options = map[string]string{"isolate": "true"}
	}
	return options
}

// ensureProjectNetwork makes the project's network and puts its database on
// it, and the edge while the project has a public app. Nothing else joins.
func (r *Runtime) ensureProjectNetwork(ctx context.Context, project, database string, public bool) error {
	name := r.networkName(project)
	inspect, err := r.engine.NetworkInspect(ctx, name, network.InspectOptions{})
	if errdefs.IsNotFound(err) {
		if _, err := r.engine.NetworkCreate(ctx, name, r.networkOptions(project)); err != nil && !errdefs.IsConflict(err) {
			return fmt.Errorf("create the project's network: %w", err)
		}
		inspect, err = r.engine.NetworkInspect(ctx, name, network.InspectOptions{})
	}
	if err != nil {
		return fmt.Errorf("read the project's network: %w", err)
	}
	if inspect.Labels[labelProject] != project {
		return fmt.Errorf("network %s does not belong to project %s", name, project)
	}
	if err := r.join(ctx, inspect, database); err != nil {
		return fmt.Errorf("attach the project's database to its network: %w", err)
	}
	if !public {
		return nil
	}
	if err := r.join(ctx, inspect, r.opts.EdgeContainer); err != nil {
		return fmt.Errorf("attach the edge to the project's network: %w", err)
	}
	return nil
}

func (r *Runtime) join(ctx context.Context, inspect network.Inspect, container string) error {
	for id, member := range inspect.Containers {
		if id == container || member.Name == container {
			return nil
		}
	}
	err := r.engine.NetworkConnect(ctx, inspect.Name, container, nil)
	if err != nil && !alreadyMember(err) {
		return err
	}
	return nil
}

// alreadyMember: a stopped container is not listed among a network's members
// but is still configured on it, and the engines refuse to add it twice.
func alreadyMember(err error) bool {
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "already exists") || strings.Contains(message, "already connected") ||
		strings.Contains(message, "already attached")
}

// removeProjectNetwork takes the database and the edge off the network first:
// a stopped database still configured on a removed network would never start.
func (r *Runtime) removeProjectNetwork(ctx context.Context, project, database string) error {
	name := r.networkName(project)
	inspect, err := r.engine.NetworkInspect(ctx, name, network.InspectOptions{})
	if errdefs.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read the project's network: %w", err)
	}
	// The proxy knows the edge by name only; every other member is managed and goes by id.
	members := []string{database, r.opts.EdgeContainer}
	for id, member := range inspect.Containers {
		if member.Name != r.opts.EdgeContainer && id != database {
			members = append(members, id)
		}
	}
	var errs []error
	for _, member := range members {
		if member == "" {
			continue
		}
		if err := r.engine.NetworkDisconnect(ctx, name, member, true); err != nil && !notMember(err) {
			errs = append(errs, fmt.Errorf("take %s off the project's network: %w", member, err))
		}
	}
	if len(errs) > 0 {
		return errors.Join(errs...)
	}
	if err := r.engine.NetworkRemove(ctx, name); err != nil && !errdefs.IsNotFound(err) {
		return fmt.Errorf("remove the project's network: %w", err)
	}
	return nil
}

func notMember(err error) bool {
	message := strings.ToLower(err.Error())
	return errdefs.IsNotFound(err) || strings.Contains(message, "is not connected") || strings.Contains(message, "not attached")
}

// Reconcile puts each serving project's database, and the edge where a route
// is public, back on the project's network: a recreated container (an upgrade)
// comes back on none of them.
func (r *Runtime) Reconcile(ctx context.Context) error {
	records, err := r.projectRecords("")
	if err != nil {
		return err
	}
	public := map[string]bool{}
	for _, record := range records {
		rendered, err := r.renderRoute(record)
		if err != nil {
			return err
		}
		public[record.Project] = public[record.Project] || rendered != ""
	}
	var errs []error
	for project, routed := range public {
		database, err := r.opts.Projects.NamespaceOf(project)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if err := r.ensureProjectNetwork(ctx, project, database, routed); err != nil {
			errs = append(errs, fmt.Errorf("project %s: %w", project, err))
		}
	}
	return errors.Join(errs...)
}

// KeepReconciled runs Reconcile every interval until ctx ends.
func (r *Runtime) KeepReconciled(ctx context.Context, interval time.Duration, report func(error)) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		if err := r.Reconcile(ctx); err != nil {
			report(err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
