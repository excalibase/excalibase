package netmeter

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/excalibase/provisioning-poc/internal/enginestats"
)

// Source reads the current counters of every app instance it can see. It may
// return readings together with an error when part of the read failed; a
// *PartialError names the instances that part would have covered.
type Source interface {
	Read(ctx context.Context) ([]Reading, error)
}

// PartialError is a read where some sources answered and some did not.
// Unobserved are the instances nobody could read this round: they may be
// running fine, so their baselines are kept rather than dropped.
type PartialError struct {
	Unobserved []string
	Err        error
}

func (e *PartialError) Error() string { return e.Err.Error() }
func (e *PartialError) Unwrap() error { return e.Err }

// AppPod says which app a pod belongs to, and the node it runs on.
type AppPod struct{ UID, ProjectID, AppID, NodeName string }

// Node is a node's name and the address its kubelet answers on.
type Node struct{ Name, Address string }

// ClusterReader is the part of the cluster the kubelet source needs.
type ClusterReader interface {
	Nodes(ctx context.Context) ([]Node, error)
	// AppPods are the customer app pods only (component and app labels), never
	// databases, jobs or functions.
	AppPods(ctx context.Context) ([]AppPod, error)
}

// SummaryFetcher reads one node's pod counters.
type SummaryFetcher interface {
	Summary(ctx context.Context, node string) ([]PodNet, error)
}

// KubeSource meters customer app pods from the kubelets' summaries.
type KubeSource struct {
	cluster ClusterReader
	fetcher SummaryFetcher
}

func NewKubeSource(cluster ClusterReader, fetcher SummaryFetcher) *KubeSource {
	return &KubeSource{cluster: cluster, fetcher: fetcher}
}

func (s *KubeSource) Read(ctx context.Context) ([]Reading, error) {
	pods, err := s.cluster.AppPods(ctx)
	if err != nil {
		return nil, fmt.Errorf("list app pods: %w", err)
	}
	nodes, err := s.cluster.Nodes(ctx)
	if err != nil {
		return nil, fmt.Errorf("list nodes: %w", err)
	}
	if len(nodes) == 0 {
		return nil, errors.New("no node to read network counters from")
	}
	owners := make(map[string]AppPod, len(pods))
	for _, pod := range pods {
		owners[pod.UID] = pod
	}
	var (
		mu       sync.Mutex
		readings []Reading
		failures []error
		failed   = map[string]bool{}
		wg       sync.WaitGroup
	)
	for _, node := range nodes {
		wg.Add(1)
		go func(node Node) {
			defer wg.Done()
			summary, err := s.fetcher.Summary(ctx, node.Address)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				failures = append(failures, err)
				failed[node.Name] = true
				return
			}
			for _, pod := range summary {
				if owner, ok := owners[pod.UID]; ok {
					readings = append(readings, Reading{Key: pod.UID, ProjectID: owner.ProjectID, AppID: owner.AppID,
						At: pod.At, Started: pod.Started, Rx: pod.Rx, Tx: pod.Tx})
				}
			}
		}(node)
	}
	wg.Wait()
	if len(failures) == 0 {
		return readings, nil
	}
	var unobserved []string
	for _, pod := range pods {
		if failed[pod.NodeName] {
			unobserved = append(unobserved, pod.UID)
		}
	}
	return readings, &PartialError{Unobserved: unobserved, Err: errors.Join(failures...)}
}

// Engine is the engine proxy's read-only container calls the Docker source uses.
type Engine interface {
	Managed(ctx context.Context) ([]enginestats.Container, error)
	Stats(ctx context.Context, ref string) (enginestats.Stats, error)
}

const (
	labelComponent = "excalibase.component"
	labelProject   = "excalibase.project"
	labelApp       = "excalibase.app"
	componentApp   = "app"
)

// DockerSource meters running customer app containers on a Docker or Podman
// host. Parity with KubeSource: the same readings, from the engine's counters.
type DockerSource struct{ engine Engine }

func NewDockerSource(engine Engine) *DockerSource { return &DockerSource{engine: engine} }

func (s *DockerSource) Read(ctx context.Context) ([]Reading, error) {
	containers, err := s.engine.Managed(ctx)
	if err != nil {
		return nil, fmt.Errorf("list containers: %w", err)
	}
	var (
		readings   []Reading
		failures   []error
		unobserved []string
	)
	for _, container := range containers {
		project, app := container.Labels[labelProject], container.Labels[labelApp]
		if container.State != "running" || container.Labels[labelComponent] != componentApp || project == "" || app == "" {
			continue
		}
		stats, err := s.engine.Stats(ctx, container.ID)
		if err != nil {
			failures = append(failures, fmt.Errorf("stats of %s: %w", container.Name, err))
			unobserved = append(unobserved, container.ID)
			continue
		}
		// Not measured is not zero: a rootless engine may report no counters,
		// or zero for both directions of a container that surely sent something.
		if !stats.NetOK || (stats.NetRx == 0 && stats.NetTx == 0) {
			continue
		}
		readings = append(readings, Reading{Key: container.ID, ProjectID: project, AppID: app, At: stats.Read, Rx: stats.NetRx, Tx: stats.NetTx})
	}
	if len(failures) == 0 {
		return readings, nil
	}
	return readings, &PartialError{Unobserved: unobserved, Err: errors.Join(failures...)}
}
