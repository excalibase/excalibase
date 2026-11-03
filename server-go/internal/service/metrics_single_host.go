package service

import (
	"context"
	"log"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/provisioner"
	"k8s.io/apimachinery/pkg/api/resource"
)

// SingleHostDatabases reads a database container on a Docker or Podman host (EXC-670).
type SingleHostDatabases interface {
	DatabaseFigures(ctx context.Context, inst *domain.DatabaseInstance) (SingleHostFigures, error)
}

// SingleHostFigures are one database container's figures; nil is not measured.
type SingleHostFigures struct {
	Running     bool
	CPUCores    *float64
	MemoryBytes *int64
	Connections *DatabaseConnections
	SizeBytes   *int64
}

// DatabaseConnections are a Postgres server's client connections.
type DatabaseConnections struct {
	Total, Idle, Max int
}

// LoopSampler is a sample the metrics loop takes on every tick.
type LoopSampler interface {
	Sample(ctx context.Context)
}

// SetSingleHost samples Docker-mode databases through their containers.
func (s *MetricsService) SetSingleHost(databases SingleHostDatabases) {
	s.singleHost = databases
}

// SetHostSampler adds the host and container sample to every tick, first.
func (s *MetricsService) SetHostSampler(sampler LoopSampler) {
	s.host = sampler
}

func (s *MetricsService) collectSingleHostMetrics(ctx context.Context, inst *domain.DatabaseInstance, metrics *domain.DatabaseMetrics) *domain.DatabaseMetrics {
	figures, err := s.singleHost.DatabaseFigures(ctx, inst)
	if err != nil {
		log.Printf("metrics of %s: database container: %v", inst.ProjectID, err)
		return metricsUnavailable(metrics, "the database container could not be read")
	}
	if !figures.Running {
		metrics.HealthStatus = "DOWN"
		return metricsUnavailable(metrics, "the database container is not running")
	}
	metrics.HealthStatus = healthOf(inst)
	one := 1
	metrics.InstanceCount = &one
	storageLimit := parseStorage(inst.Tier)
	if inst.StorageSize != "" {
		storageLimit = inst.StorageSize
	}
	metrics.StorageLimit = &storageLimit

	limits := s.containerLimits(ctx, inst)
	pod := domain.PodMetrics{Name: inst.Namespace, Role: "primary", CPULimitCores: limits.cpuCores, MemoryLimitMB: limits.memoryMB}
	if figures.CPUCores != nil {
		metrics.CPUUsageCores = figures.CPUCores
		pod.CPUCores = *figures.CPUCores
		if limits.cpuCores > 0 {
			metrics.CPUUsagePercent = float64Ptr(*figures.CPUCores / limits.cpuCores * 100)
		}
	}
	if figures.MemoryBytes != nil {
		usedMB := *figures.MemoryBytes / (1 << 20)
		metrics.MemoryUsageMB = &usedMB
		pod.MemoryMB = usedMB
		if limits.memoryMB > 0 {
			metrics.MemoryUsagePercent = float64Ptr(float64(usedMB) / float64(limits.memoryMB) * 100)
		}
	}
	if limits.cpuCores > 0 {
		metrics.CPULimitCores, metrics.MemoryLimitMB = &limits.cpuCores, &limits.memoryMB
	}
	metrics.Pods = []domain.PodMetrics{pod}
	if figures.Connections != nil {
		metrics.ActiveConnections = intPtr(figures.Connections.Total)
		metrics.IdleConnections = intPtr(figures.Connections.Idle)
		metrics.MaxConnections = intPtr(figures.Connections.Max)
	}
	if figures.SizeBytes != nil {
		fillDatabaseSize(metrics, *figures.SizeBytes, storageLimit)
	}
	return metrics
}

// fillDatabaseSize reports the data against the plan's disk, as a single
// host's volume has no size of its own.
func fillDatabaseSize(metrics *domain.DatabaseMetrics, size int64, storageLimit string) {
	gb := size / (1 << 30)
	metrics.DatabaseSizeBytes, metrics.DatabaseSizeGB = &size, &gb
	metrics.DiskUsedBytes, metrics.DiskUsageGB = &size, &gb
	quantity, err := resource.ParseQuantity(storageLimit)
	if err != nil || quantity.Value() <= 0 {
		return
	}
	limit := quantity.Value()
	metrics.DiskSizeBytes = &limit
	metrics.DiskUsagePercent = float64Ptr(float64(size) / float64(limit) * 100)
}

// containerLimits are the tier's, which the container was created with.
func (s *MetricsService) containerLimits(ctx context.Context, inst *domain.DatabaseInstance) podLimit {
	if s.tierLimits == nil {
		return podLimit{}
	}
	tier, err := s.tierLimits(ctx, inst.Tier)
	if err != nil {
		log.Printf("metrics of %s: tier limits: %v", inst.ProjectID, err)
		return podLimit{}
	}
	limits, err := provisioner.LimitsForTier(tier)
	if err != nil {
		return podLimit{}
	}
	return podLimit{cpuCores: float64(limits.NanoCPUs) / 1e9, memoryMB: limits.MemoryBytes / (1 << 20)}
}

func metricsUnavailable(metrics *domain.DatabaseMetrics, reason string) *domain.DatabaseMetrics {
	metrics.MetricsAvailable = false
	metrics.UnavailableReason = &reason
	return metrics
}

// singleHostSampled is a running database on a host whose monitoring is on.
func (s *MetricsService) singleHostSampled(inst *domain.DatabaseInstance) bool {
	return s.singleHost != nil && inst.DeploymentMode == domain.ModeDocker
}
