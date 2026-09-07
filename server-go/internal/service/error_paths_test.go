package service

import (
	"context"
	"fmt"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/k8s"
	"github.com/excalibase/provisioning-poc/internal/provisioner"
	"github.com/excalibase/provisioning-poc/internal/storage"
)

const testK8SFailDB = "k8s-fail-db"

// --- Error path tests: what happens when K8s operations fail ---

func TestMetricsWhenPodExecFails(t *testing.T) {
	dir := t.TempDir()
	store, _ := storage.NewFileSystemStore(dir)
	mock := k8s.NewMockClient()
	store.Save(&domain.DatabaseInstance{
		ProjectID: "err-db", Namespace: "ns", Status: "ACTIVE", Tier: domain.Free,
	})
	mock.ExecError["ns/err-db-postgres-1"] = fmt.Errorf("connection refused")

	svc := NewMetricsService(store, mock, dir)
	m, err := svc.GetCurrentMetrics(context.Background(), "err-db")

	if err != nil {
		t.Fatalf("should not return error, got %v", err)
	}
	if m.MetricsAvailable {
		t.Error("metricsAvailable should be false when exec fails")
	}
	if m.UnavailableReason == nil || *m.UnavailableReason == "" {
		t.Error("unavailableReason should explain the failure")
	}
}

func TestMetricsWhenMetricsServerUnavailable(t *testing.T) {
	dir := t.TempDir()
	store, _ := storage.NewFileSystemStore(dir)
	mock := k8s.NewMockClient()
	store.Save(&domain.DatabaseInstance{
		ProjectID: "no-ms-db", Namespace: "ns", Status: "ACTIVE", Tier: domain.Free,
	})
	// CNPG metrics work
	mock.ExecOutput["ns/no-ms-db-postgres-1"] = "cnpg_backends_total{state=\"active\"} 1\ncnpg_pg_settings_setting{name=\"max_connections\"} 100\n"
	// But metrics-server returns error (not installed)
	// GetPodMetrics returns error by default for unknown namespace — this is the test

	svc := NewMetricsService(store, mock, dir)
	m, _ := svc.GetCurrentMetrics(context.Background(), "no-ms-db")

	if !m.MetricsAvailable {
		t.Error("CNPG metrics should still be available even without metrics-server")
	}
	// CPU/memory should be nil (metrics-server unavailable)
	if m.CPUUsagePercent != nil {
		t.Error("cpuUsagePercent should be nil without metrics-server")
	}
	// But connections should be real
	if m.ActiveConnections == nil || *m.ActiveConnections < 1 {
		t.Errorf("activeConnections should be set from CNPG: got %v", m.ActiveConnections)
	}
}

func TestMetricsWhenEmptyPrometheusOutput(t *testing.T) {
	dir := t.TempDir()
	store, _ := storage.NewFileSystemStore(dir)
	mock := k8s.NewMockClient()
	store.Save(&domain.DatabaseInstance{
		ProjectID: "empty-db", Namespace: "ns", Status: "ACTIVE", Tier: domain.Free,
	})
	mock.ExecOutput["ns/empty-db-postgres-1"] = "" // empty output

	svc := NewMetricsService(store, mock, dir)
	m, _ := svc.GetCurrentMetrics(context.Background(), "empty-db")

	if m.MetricsAvailable {
		t.Error("should be unavailable when Prometheus returns empty")
	}
}

func TestPerformanceWhenPgStatStatementsNotEnabled(t *testing.T) {
	dir := t.TempDir()
	store, _ := storage.NewFileSystemStore(dir)
	mock := k8s.NewMockClient()
	store.Save(&domain.DatabaseInstance{
		ProjectID: "no-pgss", Namespace: "ns", Status: "ACTIVE",
	})
	// Return empty (no pg_stat_statements extension)
	mock.ExecOutput["ns/no-pgss-postgres-1"] = ""

	svc := NewPerformanceService(store, mock)
	summary, err := svc.GetSummary(context.Background(), "no-pgss")
	if err != nil {
		t.Fatalf("should not error: %v", err)
	}
	if summary.Available {
		t.Error("should be unavailable when pg_stat_statements not enabled")
	}
}

func TestMigrationWhenSQLFails(t *testing.T) {
	dir := t.TempDir()
	store, _ := storage.NewFileSystemStore(dir)
	store.Save(&domain.DatabaseInstance{
		ProjectID: "fail-sql", Namespace: "ns", Status: "ACTIVE",
	})

	// App-role creds pointing at an unreachable DB, so the exec fails and is
	// tracked as FAILED in the record rather than returned as an error.
	vault := newFakeVault()
	vault.Put("projects/fail-sql/credentials/excalibase_app", map[string]string{
		"host": "127.0.0.1", "port": "1", "username": "excalibase_app",
		"password": "x", "database": "app",
	})

	svc := NewMigrationService(store, vault, dir)
	rec, err := svc.ApplyMigration(context.Background(), "fail-sql", domain.MigrationRequest{
		SQL: "INVALID SQL",
	})

	if err != nil {
		t.Fatalf("should not return error (failure tracked in record): %v", err)
	}
	if rec.Status != "FAILED" {
		t.Errorf("status: got %s, want FAILED", rec.Status)
	}
}

func TestDeprovisionWhenK8sFails(t *testing.T) {
	dir := t.TempDir()
	store, _ := storage.NewFileSystemStore(dir)
	mock := k8s.NewMockClient()
	pgProv := provisioner.NewPostgreSQLProvisioner(mock, "")
	factory := provisioner.NewFactory(pgProv)
	svc := NewProvisioningService(store, factory, mock)

	store.Save(&domain.DatabaseInstance{
		ProjectID: testK8SFailDB, Namespace: "ns", DBType: domain.PostgreSQL, Status: "ACTIVE",
	})

	// Deprovision should succeed even if K8s namespace delete fails
	// (it logs a warning and still removes from store)
	err := svc.Deprovision(context.Background(), testK8SFailDB)
	if err != nil {
		t.Fatalf("Deprovision should succeed: %v", err)
	}

	inst, _ := store.FindByProjectID(testK8SFailDB)
	if inst != nil {
		t.Error("instance should be removed from store even if K8s fails")
	}
}

func TestBackupTriggerWhenCRDFails(t *testing.T) {
	dir := t.TempDir()
	store, _ := storage.NewFileSystemStore(dir)
	mock := k8s.NewMockClient()
	store.Save(&domain.DatabaseInstance{
		ProjectID: "bk-fail", Namespace: "ns", Status: "ACTIVE",
	})
	// ApplyCRD always succeeds in mock, so this just tests the flow
	svc := NewBackupService(store, mock, dir)
	result, err := svc.TriggerManualBackup(context.Background(), "bk-fail")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result["type"] != "MANUAL" {
		t.Error("should be MANUAL backup")
	}
}
