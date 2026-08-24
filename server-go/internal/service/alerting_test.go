package service

import (
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
)

func TestAlertingServiceAddAndGet(t *testing.T) {
	dir := t.TempDir()
	svc := NewAlertingService(dir)

	now := &domain.FlexTime{Time: time.Now()}
	svc.AddAlert(domain.Alert{
		ID: "test-1", ProjectID: "db1", Severity: "WARNING",
		Message: "CPU high", Timestamp: now,
	})

	alerts := svc.GetActiveAlerts()
	if len(alerts) != 1 {
		t.Errorf("expected 1 alert, got %d", len(alerts))
	}
	if alerts[0].ID != "test-1" {
		t.Errorf("alert ID: got %s", alerts[0].ID)
	}
}

func TestAlertingServiceProjectFilter(t *testing.T) {
	dir := t.TempDir()
	svc := NewAlertingService(dir)

	now := &domain.FlexTime{Time: time.Now()}
	svc.AddAlert(domain.Alert{ID: "a1", ProjectID: "db1", Timestamp: now})
	svc.AddAlert(domain.Alert{ID: "a2", ProjectID: "db2", Timestamp: now})

	alerts := svc.GetActiveAlertsForProject("db1")
	if len(alerts) != 1 {
		t.Errorf("expected 1 alert for db1, got %d", len(alerts))
	}
}

func TestAlertingServiceHistory(t *testing.T) {
	dir := t.TempDir()
	svc := NewAlertingService(dir)

	now := &domain.FlexTime{Time: time.Now()}
	for i := 0; i < 5; i++ {
		svc.AddAlert(domain.Alert{ID: "h" + string(rune('0'+i)), ProjectID: "db1", Timestamp: now})
	}

	hist := svc.GetAlertHistory(3)
	if len(hist) != 3 {
		t.Errorf("expected 3 in history, got %d", len(hist))
	}
}

func TestAlertingServicePersistence(t *testing.T) {
	dir := t.TempDir()
	svc1 := NewAlertingService(dir)
	now := &domain.FlexTime{Time: time.Now()}
	svc1.AddAlert(domain.Alert{ID: "persist", ProjectID: "db1", Timestamp: now})

	// Reload
	svc2 := NewAlertingService(dir)
	hist := svc2.GetAlertHistory(100)
	if len(hist) != 1 {
		t.Errorf("expected 1 persisted alert, got %d", len(hist))
	}
}

func TestCheckMetricsHighCPU(t *testing.T) {
	dir := t.TempDir()
	svc := NewAlertingService(dir)

	cpu := 95.0
	metrics := &domain.DatabaseMetrics{
		ProjectID:        "db1",
		MetricsAvailable: true,
		CPUUsagePercent:  &cpu,
	}
	svc.CheckMetrics(metrics)

	alerts := svc.GetActiveAlerts()
	if len(alerts) != 1 {
		t.Errorf("expected 1 CPU alert, got %d", len(alerts))
	}
}
