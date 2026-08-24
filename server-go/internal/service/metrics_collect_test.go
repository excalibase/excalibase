package service

import (
	"context"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/k8s"
	"github.com/excalibase/provisioning-poc/internal/storage"
)

const testPersistM = "persist-m"

func setupMetricsCollectTest(t *testing.T) (*MetricsService, *k8s.MockClient) {
	t.Helper()
	dir := t.TempDir()
	store, _ := storage.NewFileSystemStore(dir)
	mock := k8s.NewMockClient()

	store.Save(&domain.DatabaseInstance{
		ProjectID: "m-db", Namespace: "org-m-db", Status: "ACTIVE",
		DBType: domain.PostgreSQL, Tier: domain.Free,
	})
	mock.SetupPostgreSQLMock("m-db", "org-m-db", 1)

	return NewMetricsService(store, mock, dir), mock
}

func TestGetCurrentMetrics(t *testing.T) {
	svc, _ := setupMetricsCollectTest(t)
	m, err := svc.GetCurrentMetrics(context.Background(), "m-db")
	if err != nil {
		t.Fatalf("GetCurrentMetrics: %v", err)
	}
	if !m.MetricsAvailable {
		t.Errorf("should be available, reason: %v", m.UnavailableReason)
	}
	if m.ActiveConnections == nil || *m.ActiveConnections < 1 {
		t.Error("active connections should be > 0")
	}
	if m.MaxConnections == nil || *m.MaxConnections != 100 {
		t.Errorf("max connections: got %v", m.MaxConnections)
	}
	if m.HealthStatus != "HEALTHY" {
		t.Errorf("health: got %s", m.HealthStatus)
	}
	// CPU/memory from metrics-server mock
	if m.CPUUsageCores == nil {
		t.Error("cpuUsageCores should be set from metrics-server")
	}
	if m.Pods == nil || len(m.Pods) == 0 {
		t.Error("pods should have per-pod metrics")
	}
}

func TestGetCurrentMetricsNotFound(t *testing.T) {
	dir := t.TempDir()
	store, _ := storage.NewFileSystemStore(dir)
	svc := NewMetricsService(store, k8s.NewMockClient(), dir)

	_, err := svc.GetCurrentMetrics(context.Background(), "nope")
	if err == nil {
		t.Error("expected error for missing project")
	}
}

func TestGetCurrentMetricsUnavailable(t *testing.T) {
	dir := t.TempDir()
	store, _ := storage.NewFileSystemStore(dir)
	mock := k8s.NewMockClient()
	store.Save(&domain.DatabaseInstance{
		ProjectID: "fail-db", Namespace: "org-fail-db", Status: "ACTIVE",
		Tier: domain.Free,
	})
	mock.ExecError["org-fail-db/fail-db-postgres-1"] = context.DeadlineExceeded

	svc := NewMetricsService(store, mock, dir)
	m, err := svc.GetCurrentMetrics(context.Background(), "fail-db")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if m.MetricsAvailable {
		t.Error("should be unavailable when exec fails")
	}
	if m.UnavailableReason == nil {
		t.Error("unavailableReason should be set")
	}
}

func TestGetMetricsHistory(t *testing.T) {
	svc, _ := setupMetricsCollectTest(t)

	// Collect a few points
	svc.GetCurrentMetrics(context.Background(), "m-db")
	svc.GetCurrentMetrics(context.Background(), "m-db")

	hist, err := svc.GetMetricsHistory(context.Background(), "m-db", 10)
	if err != nil {
		t.Fatalf("GetMetricsHistory: %v", err)
	}
	if hist.TotalPoints < 2 {
		t.Errorf("expected >= 2 points, got %d", hist.TotalPoints)
	}
}

func TestMetricsHistoryPersistence(t *testing.T) {
	dir := t.TempDir()
	store, _ := storage.NewFileSystemStore(dir)
	mock := k8s.NewMockClient()
	store.Save(&domain.DatabaseInstance{
		ProjectID: testPersistM, Namespace: "org-persist-m", Status: "ACTIVE",
		Tier: domain.Free,
	})
	mock.SetupPostgreSQLMock(testPersistM, "org-persist-m", 1)

	svc1 := NewMetricsService(store, mock, dir)
	svc1.GetCurrentMetrics(context.Background(), testPersistM)

	// New service instance (simulates restart)
	svc2 := NewMetricsService(store, mock, dir)
	hist, _ := svc2.GetMetricsHistory(context.Background(), testPersistM, 10)
	if hist.TotalPoints < 1 {
		t.Error("history should persist to disk and reload")
	}
}

func TestMetricsStandardTierSumsReplicas(t *testing.T) {
	dir := t.TempDir()
	store, _ := storage.NewFileSystemStore(dir)
	mock := k8s.NewMockClient()
	store.Save(&domain.DatabaseInstance{
		ProjectID: "std-m", Namespace: "org-std-m", Status: "ACTIVE",
		DBType: domain.PostgreSQL, Tier: domain.Standard,
	})
	mock.SetupPostgreSQLMock("std-m", "org-std-m", 3)

	svc := NewMetricsService(store, mock, dir)
	m, _ := svc.GetCurrentMetrics(context.Background(), "std-m")

	// Each mock pod returns 3 backends (2 active + 1 idle)
	// Primary + 2 replicas = 9 total
	if m.ActiveConnections == nil || *m.ActiveConnections < 3 {
		t.Errorf("expected >= 3 connections across 3 pods, got %v", m.ActiveConnections)
	}
}
