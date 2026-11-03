package service

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/storage"
)

type stubSingleHost struct {
	figures SingleHostFigures
	err     error
	asked   []string
}

func (s *stubSingleHost) DatabaseFigures(_ context.Context, inst *domain.DatabaseInstance) (SingleHostFigures, error) {
	s.asked = append(s.asked, inst.ProjectID)
	return s.figures, s.err
}

func dockerInstance() *domain.DatabaseInstance {
	return &domain.DatabaseInstance{ProjectID: "p1", Status: "ACTIVE", DBType: domain.PostgreSQL, DeploymentMode: domain.ModeDocker,
		Namespace: "container-id", Tier: domain.Free, StorageSize: "10Gi"}
}

func singleHostMetrics(t *testing.T, figures SingleHostFigures, err error) (*MetricsService, *stubSingleHost) {
	t.Helper()
	store, _ := storage.NewFileSystemStore(t.TempDir())
	if createErr := store.Create(dockerInstance()); createErr != nil {
		t.Fatal(createErr)
	}
	svc := NewMetricsService(store, nil, t.TempDir())
	singleHost := &stubSingleHost{figures: figures, err: err}
	svc.SetSingleHost(singleHost)
	svc.SetTierLimits(func(context.Context, domain.TierType) (config.TierConfig, error) {
		return config.TierConfig{Instances: 1, CPU: "500m", Memory: "1Gi", StorageSize: "5Gi"}, nil
	})
	return svc, singleHost
}

func f64p(v float64) *float64 { return &v }
func i64p(v int64) *int64     { return &v }

func TestASingleHostDatabaseReportsItsContainerAndItsPostgres(t *testing.T) {
	svc, _ := singleHostMetrics(t, SingleHostFigures{
		Running: true, CPUCores: f64p(0.25), MemoryBytes: i64p(256 << 20),
		Connections: &DatabaseConnections{Total: 7, Idle: 5, Max: 100}, SizeBytes: i64p(2 << 30),
	}, nil)
	got, err := svc.GetCurrentMetrics(context.Background(), "p1")
	if err != nil {
		t.Fatal(err)
	}
	if !got.MetricsAvailable || got.HealthStatus != "HEALTHY" {
		t.Fatalf("metrics = %+v (%v)", got, got.UnavailableReason)
	}
	if *got.CPUUsageCores != 0.25 || *got.CPULimitCores != 0.5 || *got.CPUUsagePercent != 50 {
		t.Errorf("cpu = %v of %v = %v%%", *got.CPUUsageCores, *got.CPULimitCores, *got.CPUUsagePercent)
	}
	if *got.MemoryUsageMB != 256 || *got.MemoryLimitMB != 1024 || *got.MemoryUsagePercent != 25 {
		t.Errorf("memory = %v of %v = %v%%", *got.MemoryUsageMB, *got.MemoryLimitMB, *got.MemoryUsagePercent)
	}
	if *got.ActiveConnections != 7 || *got.IdleConnections != 5 || *got.MaxConnections != 100 {
		t.Errorf("connections = %v %v %v", *got.ActiveConnections, *got.IdleConnections, *got.MaxConnections)
	}
	if *got.DatabaseSizeBytes != 2<<30 || *got.DiskUsageGB != 2 || *got.DiskUsagePercent != 20 || *got.StorageLimit != "10Gi" || *got.InstanceCount != 1 {
		t.Errorf("size = %v, disk %v GB %v%% of %v", *got.DatabaseSizeBytes, *got.DiskUsageGB, *got.DiskUsagePercent, *got.StorageLimit)
	}
	if len(got.Pods) != 1 || got.Pods[0].Role != "primary" || got.Pods[0].CPUCores != 0.25 {
		t.Errorf("the container is the one instance: %+v", got.Pods)
	}
}

func TestAStoppedSingleHostDatabaseSaysSo(t *testing.T) {
	svc, _ := singleHostMetrics(t, SingleHostFigures{Running: false}, nil)
	got, _ := svc.GetCurrentMetrics(context.Background(), "p1")
	if got.MetricsAvailable || got.UnavailableReason == nil || !strings.Contains(*got.UnavailableReason, "not running") || got.HealthStatus != "DOWN" {
		t.Errorf("stopped = %+v", got)
	}
}

func TestASingleHostDatabaseWithoutPostgresFiguresStillReportsItsContainer(t *testing.T) {
	svc, _ := singleHostMetrics(t, SingleHostFigures{Running: true, MemoryBytes: i64p(64 << 20)}, nil)
	got, _ := svc.GetCurrentMetrics(context.Background(), "p1")
	if !got.MetricsAvailable || got.ActiveConnections != nil || got.CPUUsageCores != nil || *got.MemoryUsageMB != 64 {
		t.Errorf("partial = %+v", got)
	}
}

func TestASingleHostReadThatFailsIsUnavailable(t *testing.T) {
	svc, _ := singleHostMetrics(t, SingleHostFigures{}, errors.New("engine unreachable"))
	got, _ := svc.GetCurrentMetrics(context.Background(), "p1")
	if got.MetricsAvailable || got.UnavailableReason == nil || strings.Contains(*got.UnavailableReason, "unreachable") {
		t.Errorf("a failed read is unavailable without the internal error: %+v", got)
	}
}

func TestTheLoopSamplesSingleHostDatabasesOnlyWhenMonitoringIsOn(t *testing.T) {
	inst := dockerInstance()
	store, _ := storage.NewFileSystemStore(t.TempDir())
	svc := NewMetricsService(store, nil, t.TempDir())
	if svc.sampled(inst) {
		t.Error("without host monitoring a docker database is skipped, as before")
	}
	svc.SetSingleHost(&stubSingleHost{})
	if !svc.sampled(inst) {
		t.Error("with host monitoring a docker database is sampled")
	}
	inst.Status = "PAUSED"
	if svc.sampled(inst) {
		t.Error("a paused database is not sampled")
	}
}

type countingSampler struct{ calls int }

func (c *countingSampler) Sample(context.Context) { c.calls++ }

func TestSampleAllTakesTheHostSampleFirst(t *testing.T) {
	svc, singleHost := singleHostMetrics(t, SingleHostFigures{Running: true}, nil)
	history := &fakeMetricsHistory{}
	svc.SetHistory(history, nil)
	host := &countingSampler{}
	svc.SetHostSampler(host)
	svc.SampleAll(context.Background())
	if host.calls != 1 || len(singleHost.asked) != 1 || len(history.rows) != 1 {
		t.Errorf("host %d, databases %v, rows %d", host.calls, singleHost.asked, len(history.rows))
	}
}
