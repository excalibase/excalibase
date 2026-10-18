package main

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/k8s"
	"github.com/excalibase/provisioning-poc/internal/service"
	"github.com/excalibase/provisioning-poc/internal/storage"
	pgstore "github.com/excalibase/provisioning-poc/internal/storage/postgres"
	"github.com/excalibase/provisioning-poc/internal/testutil/fakestore"
)

// The wiring finds the tables by asserting the platform store; a renamed
// method would otherwise switch history and alerts back to the pod's disk.
var _ metricsTable = (*pgstore.Store)(nil)

type tablePlatformStore struct {
	storage.PlatformStore
	samples []domain.DatabaseMetrics
	alerts  []domain.Alert
}

func (f *tablePlatformStore) AppendMetrics(_ context.Context, m *domain.DatabaseMetrics) error {
	f.samples = append(f.samples, *m)
	return nil
}

func (f *tablePlatformStore) GetMetricsHistory(context.Context, string, int) ([]domain.DatabaseMetrics, error) {
	return f.samples, nil
}

func (f *tablePlatformStore) PruneMetricsBefore(context.Context, time.Time) error { return nil }

func (f *tablePlatformStore) SaveAlert(_ context.Context, a *domain.Alert) error {
	f.alerts = append(f.alerts, *a)
	return nil
}

func (f *tablePlatformStore) GetActiveAlerts(context.Context) ([]domain.Alert, error) {
	return f.alerts, nil
}

func (f *tablePlatformStore) GetActiveAlertsForProject(context.Context, string) ([]domain.Alert, error) {
	return f.alerts, nil
}

func (f *tablePlatformStore) GetAlertHistory(context.Context, int) ([]domain.Alert, error) {
	return f.alerts, nil
}

func TestMetricsHistoryIsKeptInThePlatformDBOnKubernetes(t *testing.T) {
	table := &tablePlatformStore{samples: []domain.DatabaseMetrics{{ProjectID: "p1"}}}
	metrics := service.NewMetricsService(fakestore.NewInstances(), k8s.NewMockClient(), t.TempDir())
	alerts := service.NewAlertingService(t.TempDir())

	wireMetricsHistory(config.AppConfig{}, table, k8s.NewMockClient(), metrics, alerts)

	history, err := metrics.GetMetricsHistory(context.Background(), "p1", 10)
	if err != nil || history.TotalPoints != 1 {
		t.Fatalf("history = %+v, err = %v; want the table's sample", history, err)
	}
	alerts.AddAlert(domain.Alert{ID: "a1", ProjectID: "p1"})
	if len(table.alerts) != 1 {
		t.Fatal("the alert was not saved to the table")
	}
}

func TestASingleHostKeepsAlertsInThePlatformDBAndSamplesNothing(t *testing.T) {
	table := &tablePlatformStore{samples: []domain.DatabaseMetrics{{ProjectID: "p1"}}}
	metrics := service.NewMetricsService(fakestore.NewInstances(), nil, t.TempDir())
	alerts := service.NewAlertingService(t.TempDir())

	wireMetricsHistory(config.AppConfig{ProvisionerMode: "docker"}, table, nil, metrics, alerts)

	if history, _ := metrics.GetMetricsHistory(context.Background(), "p1", 10); history.TotalPoints != 0 {
		t.Fatalf("history = %+v, want the file's (none)", history)
	}
	alerts.AddAlert(domain.Alert{ID: "a1", ProjectID: "p1"})
	if len(table.alerts) != 1 {
		t.Fatal("the alert was not saved to the table")
	}
}

// A platform store without the tables leaves history and alerts on disk.
func TestMetricsHistoryWiringNeedsTheTables(t *testing.T) {
	metrics := service.NewMetricsService(fakestore.NewInstances(), k8s.NewMockClient(), t.TempDir())
	alerts := service.NewAlertingService(t.TempDir())
	var bare storage.PlatformStore
	wireMetricsHistory(config.AppConfig{}, bare, k8s.NewMockClient(), metrics, alerts)
	alerts.AddAlert(domain.Alert{ID: "a1", ProjectID: "p1"})
	if got := alerts.GetActiveAlerts(); len(got) != 1 {
		t.Fatalf("active = %+v, want the alert kept in memory", got)
	}
}

// A pod without limits is measured against its tier's row, not a built-in guess.
func TestMetricsReadTierLimitsFromTheTierRows(t *testing.T) {
	body, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatalf("read main.go: %v", err)
	}
	if !strings.Contains(string(body), "metricsSvc.SetTierLimits(provSvc.TierConfig)") {
		t.Error("metrics never learn the tier rows")
	}
}
