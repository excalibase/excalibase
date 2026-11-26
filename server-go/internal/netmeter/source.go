package netmeter

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/excalibase/provisioning-poc/internal/enginestats"
)

// Source reads the current counters of every app instance it can see. It may
// return readings together with an error when part of the read failed.
type Source interface {
	Read(ctx context.Context) ([]Reading, error)
}

// AppPod says which app a pod belongs to.
type AppPod struct{ UID, ProjectID, AppID string }

// ClusterReader is the part of the cluster the kubelet source needs.
type ClusterReader interface {
	// NodeAddresses are the nodes' internal addresses.
	NodeAddresses(ctx context.Context) ([]string, error)
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
	nodes, err := s.cluster.NodeAddresses(ctx)
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
		wg       sync.WaitGroup
	)
	for _, node := range nodes {
		wg.Add(1)
		go func(node string) {
			defer wg.Done()
			summary, err := s.fetcher.Summary(ctx, node)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				failures = append(failures, err)
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
	return readings, errors.Join(failures...)
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
		readings []Reading
		failures []error
	)
	for _, container := range containers {
		project, app := container.Labels[labelProject], container.Labels[labelApp]
		if container.State != "running" || container.Labels[labelComponent] != componentApp || project == "" || app == "" {
			continue
		}
		stats, err := s.engine.Stats(ctx, container.ID)
		if err != nil {
			failures = append(failures, fmt.Errorf("stats of %s: %w", container.Name, err))
			continue
		}
		if !stats.NetOK {
			// Not measured is not zero: a rootless engine may report no counters.
			continue
		}
		readings = append(readings, Reading{Key: container.ID, ProjectID: project, AppID: app, At: stats.Read, Rx: stats.NetRx, Tx: stats.NetTx})
	}
	return readings, errors.Join(failures...)
}
