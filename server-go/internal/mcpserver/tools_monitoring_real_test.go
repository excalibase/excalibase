package mcpserver

import (
	"context"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/features"
	"github.com/excalibase/provisioning-poc/internal/handler"
	"github.com/excalibase/provisioning-poc/internal/k8s"
	"github.com/excalibase/provisioning-poc/internal/service"
	"github.com/excalibase/provisioning-poc/internal/storage"
	"github.com/go-chi/chi/v5"
)

type fixedCharts struct{ charts *domain.RequestCharts }

func (f fixedCharts) Chart(context.Context, string, string) (*domain.RequestCharts, error) {
	return f.charts, nil
}

// The monitoring tools read what the real handlers write, not a fixture
// shaped by hand (EXC-684 review).
func TestMonitoringToolsReadTheRealHandlers(t *testing.T) {
	dir := t.TempDir()
	instances, err := storage.NewFileSystemStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := instances.Create(&domain.DatabaseInstance{ProjectID: testProjectA, Namespace: "ns", Status: "ACTIVE", Tier: domain.Free}); err != nil {
		t.Fatal(err)
	}
	p95 := 20.0
	charts := &domain.RequestCharts{
		ProjectID: testProjectA, Range: "1h", From: time.Unix(0, 0).UTC(), To: time.Unix(3600, 0).UTC(), StepSeconds: 60,
		Points: []domain.RequestChartPoint{{Timestamp: time.Unix(60, 0).UTC(), Family: "rest", Requests: 7, P95Ms: &p95}},
		Totals: []domain.RequestChartPoint{{Family: "rest", Requests: 7}},
	}
	alerts := service.NewAlertingService(dir)
	alerts.AddAlert(domain.Alert{ID: "a1", ProjectID: testProjectA, Severity: "WARNING", Message: "cpu high", Metric: "cpu"})
	metrics := service.NewMetricsService(instances, k8s.NewMockClient(), dir)

	router := chi.NewRouter()
	router.Get("/api/provision/{projectId}/requests", handler.NewRequestMetricsHandler(fixedCharts{charts}).Charts)
	router.Route("/api/provision/{projectId}/metrics", handler.NewMetricsHandler(metrics).Routes)
	router.Route("/api/alerts/project/{projectId}", handler.NewAlertHandler(alerts).ProjectRoutes)

	settings := testSettings
	settings.Features = features.NewStatic(features.RequestMetrics)
	cs := sessionWith(t, router, &recordingAudit{}, writeCaller(), settings)

	got := structured(t, callTool(t, cs, "get_request_metrics", map[string]any{"project_id": testProjectA}))
	points, _ := got["points"].([]any)
	if got["range"] != "1h" || len(points) != 1 {
		t.Fatalf("request metrics = %v", got)
	}
	if point, _ := points[0].(map[string]any); point["family"] != "rest" || point["p95Ms"] != 20.0 || point["requests"] != 7.0 {
		t.Errorf("point = %v", points[0])
	}

	got = structured(t, callTool(t, cs, "list_alerts", map[string]any{"project_id": testProjectA}))
	listed, _ := got["alerts"].([]any)
	if len(listed) != 1 {
		t.Fatalf("alerts = %v", got)
	}
	if alert, _ := listed[0].(map[string]any); alert["severity"] != "WARNING" || alert["message"] != "cpu high" {
		t.Errorf("alert = %v", listed[0])
	}

	got = structured(t, callTool(t, cs, "get_metrics", map[string]any{"project_id": testProjectA, "history": 5}))
	current, _ := got["current"].(map[string]any)
	history, _ := got["history"].(map[string]any)
	if current["metricsAvailable"] == nil || history["metrics"] == nil {
		t.Errorf("metrics = %v", got)
	}
}
