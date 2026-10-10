package dockerapps

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	"github.com/containerd/errdefs"
	"github.com/docker/docker/api/types/container"

	"github.com/excalibase/provisioning-poc/internal/k8s"
)

// hostNode names the one node a single host is.
const hostNode = "single-host"

// GetClusterCapacity is the host's CPUs and memory against what runs on it:
// app containers at their plan's requests, every other managed container
// (the databases) at its limits, which is all it declares.
func (r *Runtime) GetClusterCapacity(ctx context.Context) (k8s.ClusterCapacity, error) {
	info, err := r.engine.Info(ctx)
	if err != nil {
		return k8s.ClusterCapacity{}, fmt.Errorf("read the host's capacity: %w", err)
	}
	running, err := r.engine.ContainerList(ctx, container.ListOptions{Filters: labelFilter()})
	if err != nil {
		return k8s.ClusterCapacity{}, fmt.Errorf("list running containers: %w", err)
	}
	node := k8s.NodeCapacity{Name: hostNode, AllocatableCPUMilli: int64(info.NCPU) * 1000, AllocatableMemBytes: info.MemTotal}
	for _, summary := range running {
		cpu, memory, err := r.charge(ctx, summary)
		if err != nil {
			return k8s.ClusterCapacity{}, err
		}
		node.RequestedCPUMilli += cpu
		node.RequestedMemBytes += memory
	}
	return k8s.ClusterCapacity{
		AllocatableCPUMilli: node.AllocatableCPUMilli, AllocatableMemBytes: node.AllocatableMemBytes,
		RequestedCPUMilli: node.RequestedCPUMilli, RequestedMemBytes: node.RequestedMemBytes,
		Nodes: []k8s.NodeCapacity{node},
	}, nil
}

func (r *Runtime) charge(ctx context.Context, summary container.Summary) (int64, int64, error) {
	if summary.Labels[labelComponent] == componentApp {
		return requestOf(summary.Labels)
	}
	inspect, err := r.engine.ContainerInspect(ctx, summary.ID)
	if errdefs.IsNotFound(err) {
		return 0, 0, nil
	}
	if err != nil {
		return 0, 0, fmt.Errorf("read %s: %w", nameOf(summary), err)
	}
	if inspect.HostConfig == nil {
		return 0, 0, fmt.Errorf("read %s: the engine answered no limits", nameOf(summary))
	}
	return inspect.HostConfig.NanoCPUs / 1_000_000, inspect.HostConfig.Memory, nil
}

func requestOf(labels map[string]string) (int64, int64, error) {
	cpu, cpuErr := strconv.ParseInt(labels[labelCPURequest], 10, 64)
	memory, memoryErr := strconv.ParseInt(labels[labelMemoryRequest], 10, 64)
	if err := errors.Join(cpuErr, memoryErr); err != nil {
		return 0, 0, fmt.Errorf("app container %s has no readable request: %w", labels[labelApp], err)
	}
	return cpu, memory, nil
}

// LiveAppPods is what the app's running containers request.
func (r *Runtime) LiveAppPods(ctx context.Context, namespace, appID string) (k8s.AppPods, error) {
	project, err := r.opts.Projects.ProjectOf(namespace)
	if err != nil {
		return k8s.AppPods{}, err
	}
	list, err := r.listApps(ctx, labelProject, project, labelApp, appID)
	if err != nil {
		return k8s.AppPods{}, err
	}
	var live k8s.AppPods
	for _, summary := range where(list, isUp) {
		cpu, memory, err := requestOf(summary.Labels)
		if err != nil {
			return k8s.AppPods{}, err
		}
		live.Count++
		live.CPUMilli += cpu
		live.MemBytes += memory
		live.MaxCPUMilli, live.MaxMemBytes = max(live.MaxCPUMilli, cpu), max(live.MaxMemBytes, memory)
	}
	return live, nil
}

// TeardownProject removes everything the project's apps left on the host:
// routes first, then containers, disks and the project's network.
// It works from the project id: a project whose database never started has no scope.
func (r *Runtime) TeardownProject(ctx context.Context, project, namespace string) error {
	records, err := r.projectRecords(project)
	if err != nil {
		return err
	}
	for _, record := range records {
		if err := r.removeRecord(project, record.AppID); err != nil {
			return err
		}
	}
	list, err := r.listApps(ctx, labelProject, project)
	if err != nil {
		return err
	}
	if err := r.retire(ctx, list); err != nil {
		return err
	}
	if err := r.removeVolumes(ctx, labelProject, project); err != nil {
		return err
	}
	return r.removeProjectNetwork(ctx, project, namespace)
}
