package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/features"
	"github.com/excalibase/provisioning-poc/internal/handler"
	"github.com/excalibase/provisioning-poc/internal/hostmetrics"
	"github.com/excalibase/provisioning-poc/internal/hostmonitor"
	"github.com/excalibase/provisioning-poc/internal/service"
	"github.com/excalibase/provisioning-poc/internal/storage"
)

// monitoringTables stands in for the platform DB's monitoring tables.
type monitoringTables struct{ storage.PlatformStore }

func (monitoringTables) AppendHostSample(context.Context, hostmetrics.Sample) error { return nil }
func (monitoringTables) AppendContainerSamples(context.Context, []domain.ContainerSample) error {
	return nil
}
func (monitoringTables) HostAverages(context.Context, time.Time) (hostmetrics.Averages, error) {
	return hostmetrics.Averages{}, nil
}
func (monitoringTables) PruneHostMonitoring(context.Context, time.Time) error { return nil }
func (monitoringTables) GetMonitoringAlertSettings(context.Context) (domain.MonitoringAlertSettings, bool, error) {
	return domain.MonitoringAlertSettings{}, false, nil
}
func (monitoringTables) SaveMonitoringAlertSettings(context.Context, domain.MonitoringAlertSettings) error {
	return nil
}
func (monitoringTables) LatestHostSample(context.Context) (*hostmetrics.Sample, error) {
	return nil, nil
}
func (monitoringTables) HostHistory(context.Context, time.Time, time.Duration) ([]hostmetrics.Sample, error) {
	return []hostmetrics.Sample{}, nil
}
func (monitoringTables) LatestContainerSamples(context.Context) ([]domain.ContainerSample, *time.Time, error) {
	return []domain.ContainerSample{}, nil, nil
}
func (monitoringTables) ContainerHistory(context.Context, string, time.Time, time.Duration) ([]domain.ContainerSample, error) {
	return []domain.ContainerSample{}, nil
}
func (monitoringTables) ProjectContainerHistory(context.Context, string, time.Time, time.Duration) ([]domain.ContainerSample, error) {
	return []domain.ContainerSample{}, nil
}
func (monitoringTables) LogAudit(context.Context, *domain.AuditEntry) error { return nil }

// matrixHostMonitoring is the handler the authorization matrix drives.
func matrixHostMonitoring() *handler.HostMonitoringHandler {
	tables := monitoringTables{}
	return handler.NewHostMonitoringHandler(tables, hostmonitor.NewSettings(tables, nil), tables)
}

func monitoringDeps(t *testing.T, mode string, on bool) hostMonitoringDeps {
	t.Helper()
	cfg := config.AppConfig{ProvisionerMode: mode, DockerHost: "tcp://engine-proxy:2375"}
	if on {
		cfg.DarkFeatures = []features.Feature{features.HostMetrics}
	}
	return hostMonitoringDeps{cfg: cfg, sqlStore: monitoringTables{}, metrics: service.NewMetricsService(nil, nil, t.TempDir()),
		alerts: service.NewAlertingService(t.TempDir())}
}

func TestHostMonitoringIsBuiltOnlyOnASingleHostWithTheFlagOn(t *testing.T) {
	if wireHostMonitoring(monitoringDeps(t, "docker", true)) == nil {
		t.Error("docker with hostmetrics on builds host monitoring")
	}
	for name, deps := range map[string]hostMonitoringDeps{
		"flag off":                 monitoringDeps(t, "docker", false),
		"kubernetes with the flag": monitoringDeps(t, "kubernetes", true),
	} {
		if wireHostMonitoring(deps) != nil {
			t.Errorf("%s: host monitoring was built", name)
		}
	}
	noTables := monitoringDeps(t, "docker", true)
	noTables.sqlStore = nil
	if wireHostMonitoring(noTables) != nil {
		t.Error("without the monitoring tables nothing is built")
	}
	noProxy := monitoringDeps(t, "docker", true)
	noProxy.cfg.DockerHost = "unix:///var/run/docker.sock"
	if wireHostMonitoring(noProxy) != nil {
		t.Error("provisioning reaches the engine only through the proxy")
	}
}

func TestHostMonitoringRoutesAreAbsentWithoutTheHandler(t *testing.T) {
	r := chi.NewRouter()
	r.Route("/api/admin", func(r chi.Router) { mountHostMonitoring(r, &handlerDeps{}) })
	mountProjectContainers(r, nil, nil, &handlerDeps{})
	for _, path := range []string{"/api/admin/monitoring/host", "/api/projects/p1/containers/metrics"} {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
		if w.Code != http.StatusNotFound {
			t.Errorf("%s = %d, want unmounted", path, w.Code)
		}
	}
}
