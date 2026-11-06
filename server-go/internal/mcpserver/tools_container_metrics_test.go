package mcpserver

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/features"
	"github.com/excalibase/provisioning-poc/internal/handler"
	"github.com/excalibase/provisioning-poc/internal/hostmetrics"
	"github.com/go-chi/chi/v5"
)

var sampledAt = time.Date(2026, 11, 6, 3, 0, 0, 0, time.UTC)

type fakeContainerReads struct{ rangeSince time.Time }

func (f *fakeContainerReads) LatestHostSample(context.Context) (*hostmetrics.Sample, error) {
	return nil, nil
}

func (f *fakeContainerReads) HostHistory(context.Context, time.Time, time.Duration) ([]hostmetrics.Sample, error) {
	return nil, nil
}

func (f *fakeContainerReads) LatestContainerSamples(context.Context) ([]domain.ContainerSample, *time.Time, error) {
	cores, percent, memory, limit := 0.5, 25.0, int64(512<<20), int64(1<<30)
	at := sampledAt
	return []domain.ContainerSample{
		{Name: "excalibase-proj-a-postgres", ProjectID: testProjectA, Component: domain.ContainerDatabase, State: "running", Health: "healthy",
			Timestamp: at, CPUCores: &cores, CPUPercent: &percent, MemoryBytes: &memory, MemoryLimitBytes: &limit},
		{Name: "excalibase-other-postgres", ProjectID: "other", Component: domain.ContainerDatabase, State: "running", Timestamp: at},
	}, &at, nil
}

func (f *fakeContainerReads) ContainerHistory(context.Context, string, time.Time, time.Duration) ([]domain.ContainerSample, error) {
	return nil, nil
}

func (f *fakeContainerReads) ProjectContainerHistory(_ context.Context, projectID string, since time.Time, _ time.Duration) ([]domain.ContainerSample, error) {
	f.rangeSince = since
	return []domain.ContainerSample{{Name: "excalibase-proj-a-postgres", ProjectID: projectID, Timestamp: sampledAt.Add(-time.Minute)}}, nil
}

func containerMetricsRouter() http.Handler {
	router := chi.NewRouter()
	router.Get("/api/provision/{projectId}/metrics/current", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"metricsAvailable":true,"cpuUsagePercent":10}`))
	})
	router.Get("/api/projects/{projectId}/containers/metrics", handler.NewHostMonitoringHandler(&fakeContainerReads{}, nil, nil).ProjectContainers)
	return router
}

// The tool reads what the real handler writes, and only the project's own containers (EXC-670).
func TestGetMetricsFoldsInContainerMetricsOnAHostThatReportsThem(t *testing.T) {
	settings := testSettings
	settings.Features = features.NewStatic(features.HostMetrics)
	cs := sessionWith(t, containerMetricsRouter(), &recordingAudit{}, writeCaller(), settings)
	out := structured(t, callTool(t, cs, "get_metrics", map[string]any{"project_id": testProjectA}))
	containers, _ := out["containers"].([]any)
	if len(containers) != 1 {
		t.Fatalf("containers = %v", out["containers"])
	}
	container, _ := containers[0].(map[string]any)
	current, _ := container["current"].(map[string]any)
	if container["name"] != "excalibase-proj-a-postgres" || container["state"] != "running" || container["health"] != "healthy" || current["cpuPercent"] != 25.0 || current["memoryBytes"] != float64(512<<20) {
		t.Fatalf("container = %v", container)
	}
	if _, ok := container["points"]; ok {
		t.Errorf("points are returned without container_range: %v", container)
	}
	if strings.Contains(realJSON(out, nil), "excalibase-other-postgres") {
		t.Error("another project's container is shown")
	}
}

func TestGetMetricsReturnsContainerPointsForARange(t *testing.T) {
	settings := testSettings
	settings.Features = features.NewStatic(features.HostMetrics)
	cs := sessionWith(t, containerMetricsRouter(), &recordingAudit{}, writeCaller(), settings)
	out := structured(t, callTool(t, cs, "get_metrics", map[string]any{"project_id": testProjectA, "container_range": "6h"}))
	containers, _ := out["containers"].([]any)
	container, _ := containers[0].(map[string]any)
	if points, _ := container["points"].([]any); len(points) != 1 || out["containerRange"] != "6h" {
		t.Fatalf("out = %v", out)
	}
	if res := callTool(t, cs, "get_metrics", map[string]any{"project_id": testProjectA, "container_range": "2y"}); !res.IsError {
		t.Error("an unknown range was accepted")
	}
}

func TestGetMetricsLeavesContainersOutWhileTheFlagIsOff(t *testing.T) {
	routes := newFakeRoutes()
	routes.on(http.MethodGet, provisionA+"/metrics/current", 200, `{"metricsAvailable":true}`)
	cs := session(t, routes, &recordingAudit{}, writeCaller())
	out := structured(t, callTool(t, cs, "get_metrics", map[string]any{"project_id": testProjectA}))
	if _, ok := out["containers"]; ok || len(routes.calls) != 1 {
		t.Errorf("out = %v, calls = %d", out, len(routes.calls))
	}
}

// A platform that does not report containers (Kubernetes) leaves the database metrics whole.
func TestGetMetricsKeepsTheDatabaseAnswerWhenContainersAreNotReported(t *testing.T) {
	routes := newFakeRoutes()
	routes.on(http.MethodGet, provisionA+"/metrics/current", 200, `{"metricsAvailable":true}`)
	settings := testSettings
	settings.Features = features.NewStatic(features.HostMetrics)
	cs := sessionWith(t, routes, &recordingAudit{}, writeCaller(), settings)
	out := structured(t, callTool(t, cs, "get_metrics", map[string]any{"project_id": testProjectA}))
	if out["current"] == nil || out["containers"] != nil || out["containersNote"] == nil {
		t.Fatalf("out = %v", out)
	}
}
