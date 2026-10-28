// Package hostmonitor samples a single Docker or Podman host and every
// container on it once a minute (EXC-670): the host from its own /proc and
// /sys, the containers through the engine proxy's read-only stats routes.
// It stores the rows, raises and resolves alerts and sends notices.
package hostmonitor

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/enginestats"
	"github.com/excalibase/provisioning-poc/internal/hostmetrics"
	"github.com/excalibase/provisioning-poc/internal/provisioner"
	"github.com/excalibase/provisioning-poc/internal/service"
)

// StateMissing is a platform container the engine does not have.
const StateMissing = "missing"

const (
	defaultConcurrency  = 4
	defaultStatsTimeout = 5 * time.Second
	pruneEvery          = time.Hour
	componentLabel      = "excalibase.component"
	projectLabel        = "excalibase.project"
	// diskToolComponent is a short-lived helper of app disks, not a workload.
	diskToolComponent = "app-disk-tool"
)

// Engine is the engine proxy's read-only container calls.
type Engine interface {
	Managed(ctx context.Context) ([]enginestats.Container, error)
	Inspect(ctx context.Context, ref string) (enginestats.Container, error)
	Stats(ctx context.Context, ref string) (enginestats.Stats, error)
}

// Store keeps the samples and the alert rules.
type Store interface {
	AppendHostSample(ctx context.Context, sample hostmetrics.Sample) error
	AppendContainerSamples(ctx context.Context, samples []domain.ContainerSample) error
	HostAverages(ctx context.Context, since time.Time) (hostmetrics.Averages, error)
	PruneHostMonitoring(ctx context.Context, now time.Time) error
	GetMonitoringAlertSettings(ctx context.Context) (domain.MonitoringAlertSettings, bool, error)
}

// Instances lists projects, to attribute containers and expect databases.
type Instances interface {
	FindAll() ([]*domain.DatabaseInstance, error)
}

// AlertNotifier sends a raised or resolved alert to the configured channels.
type AlertNotifier interface {
	Notify(ctx context.Context, settings domain.MonitoringAlertSettings, change service.AlertChange)
}

// Deps builds a Monitor.
type Deps struct {
	Engine    Engine
	Store     Store
	Instances Instances
	Alerts    *service.AlertingService
	Notifier  AlertNotifier
	Paths     hostmetrics.Paths
	// Platform are the platform's own long-running containers, by name.
	Platform []string
}

// Monitor is the single-host sampler; Sample runs on the metrics loop.
type Monitor struct {
	deps        Deps
	now         func() time.Time
	concurrency int
	timeout     time.Duration

	mu        sync.Mutex
	prevHost  *hostmetrics.Counters
	prevStats map[string]enginestats.Stats
	// latest is read by the database metrics while a sample runs.
	latestMu  sync.RWMutex
	latest    map[string]domain.ContainerSample
	badSince  map[string]time.Time
	lastPrune time.Time
	problems  string
}

func New(deps Deps) *Monitor {
	return &Monitor{
		deps: deps, now: time.Now, concurrency: defaultConcurrency, timeout: defaultStatsTimeout,
		prevStats: map[string]enginestats.Stats{}, latest: map[string]domain.ContainerSample{}, badSince: map[string]time.Time{},
	}
}

// observed is one container as listed, with what it is to the platform.
type observed struct {
	container enginestats.Container
	projectID string
	component string
	stats     *enginestats.Stats
}

// Sample takes one minute: the host, every container, alerts, and the hourly prune.
func (m *Monitor) Sample(ctx context.Context) {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.now()
	counters, problems := hostmetrics.Read(m.deps.Paths, now)
	m.logProblems(problems)
	instances, err := m.deps.Instances.FindAll()
	if err != nil {
		log.Printf("host monitor: list projects: %v", err)
	}
	containers := m.observe(ctx, instances)
	m.takeStats(ctx, containers)

	samples := m.containerSamples(now, counters, containers)
	host := hostmetrics.Rates(m.prevHost, counters)
	if !counters.NetOK {
		addContainerNetwork(&host, samples)
	}
	m.prevHost = &counters
	if err := m.deps.Store.AppendHostSample(ctx, host); err != nil {
		log.Printf("host monitor: record host: %v", err)
	}
	if err := m.deps.Store.AppendContainerSamples(ctx, samples); err != nil {
		log.Printf("host monitor: record containers: %v", err)
	}
	m.evaluate(ctx, now, instances, samples)
	if now.Sub(m.lastPrune) >= pruneEvery {
		m.lastPrune = now
		if err := m.deps.Store.PruneHostMonitoring(ctx, now); err != nil {
			log.Printf("host monitor: prune: %v", err)
		}
	}
}

// Latest is a container's newest sample, by engine id.
func (m *Monitor) Latest(containerID string) (domain.ContainerSample, bool) {
	m.latestMu.RLock()
	defer m.latestMu.RUnlock()
	sample, ok := m.latest[containerID]
	return sample, ok
}

// logProblems logs what the host could not give only when it changes.
func (m *Monitor) logProblems(problems []error) {
	text := errors.Join(problems...)
	joined := ""
	if text != nil {
		joined = text.Error()
	}
	if joined != m.problems && joined != "" {
		log.Printf("host monitor: host figures not read: %v", strings.ReplaceAll(joined, "\n", "; "))
	}
	m.problems = joined
}

// observe lists the managed containers and inspects the platform's.
func (m *Monitor) observe(ctx context.Context, instances []*domain.DatabaseInstance) []*observed {
	attribute := attribution(instances)
	var containers []*observed
	managed, err := m.deps.Engine.Managed(ctx)
	if err != nil {
		log.Printf("host monitor: list containers: %v", err)
	}
	for _, container := range managed {
		if container.Labels[componentLabel] == diskToolComponent {
			continue
		}
		if container.State == "running" && container.Health == "" {
			// Podman's list leaves the health out.
			if inspected, err := m.deps.Engine.Inspect(ctx, container.ID); err == nil {
				container.Health = inspected.Health
			}
		}
		projectID, component := attribute(container)
		containers = append(containers, &observed{container: container, projectID: projectID, component: component})
	}
	for _, name := range m.deps.Platform {
		container, err := m.deps.Engine.Inspect(ctx, name)
		if err != nil {
			if !errors.Is(err, enginestats.ErrNotFound) {
				log.Printf("host monitor: inspect %s: %v", name, err)
			}
			container = enginestats.Container{Name: name, State: StateMissing}
		}
		container.Name = name
		containers = append(containers, &observed{container: container, component: domain.ContainerPlatform})
	}
	sort.Slice(containers, func(i, j int) bool { return containers[i].container.Name < containers[j].container.Name })
	return containers
}

// takeStats samples every running container, a few at a time.
func (m *Monitor) takeStats(ctx context.Context, containers []*observed) {
	slots := make(chan struct{}, m.concurrency)
	var wg sync.WaitGroup
	for _, entry := range containers {
		if entry.container.State != "running" {
			continue
		}
		wg.Add(1)
		slots <- struct{}{}
		go func(entry *observed) {
			defer wg.Done()
			defer func() { <-slots }()
			statsCtx, cancel := context.WithTimeout(ctx, m.timeout)
			defer cancel()
			ref := entry.container.ID
			if entry.component == domain.ContainerPlatform {
				// The proxy admits platform containers by name only.
				ref = entry.container.Name
			}
			stats, err := m.deps.Engine.Stats(statsCtx, ref)
			switch {
			case errors.Is(err, enginestats.ErrNotRunning):
				entry.container.State = "exited"
			case err != nil:
				log.Printf("host monitor: stats of %s: %v", entry.container.Name, err)
			default:
				entry.stats = &stats
			}
		}(entry)
	}
	wg.Wait()
}

func (m *Monitor) containerSamples(now time.Time, host hostmetrics.Counters, containers []*observed) []domain.ContainerSample {
	samples := make([]domain.ContainerSample, 0, len(containers))
	nextStats := map[string]enginestats.Stats{}
	nextLatest := map[string]domain.ContainerSample{}
	for _, entry := range containers {
		sample := domain.ContainerSample{
			Name: entry.container.Name, ProjectID: entry.projectID, Component: entry.component,
			State: entry.container.State, Health: entry.container.Health, Timestamp: now,
		}
		key := entry.container.ID
		if key == "" {
			key = entry.container.Name
		}
		if entry.stats != nil {
			prev, seen := m.prevStats[key]
			fillFigures(&sample, entry.stats, prevIf(prev, seen), host)
			nextStats[key] = *entry.stats
		}
		nextLatest[key] = sample
		samples = append(samples, sample)
	}
	m.prevStats = nextStats
	m.latestMu.Lock()
	m.latest = nextLatest
	m.latestMu.Unlock()
	return samples
}

func prevIf(prev enginestats.Stats, seen bool) *enginestats.Stats {
	if !seen {
		return nil
	}
	return &prev
}

func fillFigures(sample *domain.ContainerSample, stats, prev *enginestats.Stats, host hostmetrics.Counters) {
	memory := int64(stats.MemoryBytes)
	sample.MemoryBytes = &memory
	// The engine reports the host's memory for a container without a limit.
	if stats.MemoryLimitBytes > 0 && (!host.MemOK || stats.MemoryLimitBytes < host.MemTotal) {
		limit := int64(stats.MemoryLimitBytes)
		sample.MemoryLimitBytes = &limit
	}
	if prev == nil {
		return
	}
	seconds := stats.Read.Sub(prev.Read).Seconds()
	if seconds <= 0 {
		return
	}
	if stats.CPUNanos >= prev.CPUNanos {
		cores := float64(stats.CPUNanos-prev.CPUNanos) / 1e9 / seconds
		sample.CPUCores = &cores
		if cpus := hostCPUs(host, stats); cpus > 0 {
			percent := cores / float64(cpus) * 100
			sample.CPUPercent = &percent
		}
	}
	if stats.NetOK && prev.NetOK && stats.NetRx >= prev.NetRx && stats.NetTx >= prev.NetTx {
		rx, tx := float64(stats.NetRx-prev.NetRx)/seconds, float64(stats.NetTx-prev.NetTx)/seconds
		sample.NetworkRxBytesPerSec, sample.NetworkTxBytesPerSec = &rx, &tx
	}
}

func hostCPUs(host hostmetrics.Counters, stats *enginestats.Stats) int {
	if host.CPUCount > 0 {
		return host.CPUCount
	}
	return stats.OnlineCPUs
}

// addContainerNetwork stands in for the host's interfaces with the containers' traffic.
func addContainerNetwork(host *hostmetrics.Sample, samples []domain.ContainerSample) {
	var rx, tx float64
	measured := false
	for _, sample := range samples {
		if sample.NetworkRxBytesPerSec != nil && sample.NetworkTxBytesPerSec != nil {
			rx += *sample.NetworkRxBytesPerSec
			tx += *sample.NetworkTxBytesPerSec
			measured = true
		}
	}
	if !measured {
		return
	}
	source := hostmetrics.NetworkFromContainers
	host.NetworkRxBytesPerSec, host.NetworkTxBytesPerSec, host.NetworkSource = &rx, &tx, &source
}

// attribution names each managed container's project and role from the
// labels apps carry, a database's container id, or the names provisioning gives.
func attribution(instances []*domain.DatabaseInstance) func(enginestats.Container) (string, string) {
	byID := map[string]string{}
	byName := map[string][2]string{}
	for _, inst := range instances {
		if inst.DeploymentMode != domain.ModeDocker {
			continue
		}
		if inst.Namespace != "" {
			byID[inst.Namespace] = inst.ProjectID
		}
		for _, name := range []string{provisioner.DatabaseContainerName(inst.ProjectID), provisioner.DatabaseContainerName(inst.ProjectName), provisioner.MySQLContainerName(inst.ProjectID)} {
			byName[name] = [2]string{inst.ProjectID, domain.ContainerDatabase}
		}
		byName[provisioner.DocumentDBGatewayName(inst.ProjectID)] = [2]string{inst.ProjectID, domain.ContainerDocumentDB}
		byName[provisioner.WatcherContainerName(inst.ProjectID)] = [2]string{inst.ProjectID, domain.ContainerWatcher}
	}
	return func(container enginestats.Container) (string, string) {
		if projectID := container.Labels[projectLabel]; projectID != "" {
			component := container.Labels[componentLabel]
			if component != domain.ContainerApp {
				component = domain.ContainerOther
			}
			return projectID, component
		}
		if projectID, ok := byID[container.ID]; ok {
			return projectID, domain.ContainerDatabase
		}
		if known, ok := byName[container.Name]; ok {
			return known[0], known[1]
		}
		return "", domain.ContainerOther
	}
}

// containerAlertMetric keys a container's down alert.
func containerAlertMetric(name string) string {
	return fmt.Sprintf("container.down:%s", name)
}
