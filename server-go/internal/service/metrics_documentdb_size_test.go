package service

import (
	"context"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/k8s"
	"github.com/excalibase/provisioning-poc/internal/storage"
)

// EXC-531: a DocumentDB project keeps its documents in the postgres
// database, so its size counts that database as well as app.

const (
	appBytes      = 8388608
	postgresBytes = 50331648
)

func metricsWithDatabases(t *testing.T, documentDB bool) *domain.DatabaseMetrics {
	t.Helper()
	dir := t.TempDir()
	store, _ := storage.NewFileSystemStore(dir)
	mock := k8s.NewMockClient()
	if err := store.Create(&domain.DatabaseInstance{
		ProjectID: "size-db", Namespace: "org-size-db", Status: "ACTIVE",
		DBType: domain.PostgreSQL, Tier: domain.Free, DocumentDB: documentDB, DatabaseName: "app",
	}); err != nil {
		t.Fatalf("create: %v", err)
	}
	mock.SetupPostgreSQLMock("size-db", "org-size-db", 1)
	mock.ExecOutput["org-size-db/size-db-postgres-1"] = `cnpg_backends_total{state="active",datname="app"} 1
cnpg_pg_database_size_bytes{datname="app"} 8388608
cnpg_pg_database_size_bytes{datname="postgres"} 50331648
cnpg_pg_settings_setting{name="max_connections"} 100
`
	metrics, err := NewMetricsService(store, mock, dir).GetCurrentMetrics(context.Background(), "size-db")
	if err != nil {
		t.Fatalf("GetCurrentMetrics: %v", err)
	}
	return metrics
}

func TestADocumentDBProjectsSizeCountsItsDocuments(t *testing.T) {
	metrics := metricsWithDatabases(t, true)
	if metrics.DatabaseSizeBytes == nil || *metrics.DatabaseSizeBytes != appBytes+postgresBytes {
		t.Fatalf("databaseSizeBytes = %v, want %d (app + postgres)", metrics.DatabaseSizeBytes, appBytes+postgresBytes)
	}
}

func TestAPostgresProjectsSizeIsItsAppDatabase(t *testing.T) {
	metrics := metricsWithDatabases(t, false)
	if metrics.DatabaseSizeBytes == nil || *metrics.DatabaseSizeBytes != appBytes {
		t.Fatalf("databaseSizeBytes = %v, want %d (app only)", metrics.DatabaseSizeBytes, appBytes)
	}
}

func TestADocumentDBSizeWithoutItsDocumentsFigureIsNotReported(t *testing.T) {
	labeled := parseLabeledMetrics(`cnpg_pg_database_size_bytes{datname="app"} 8388608` + "\n")
	if size, ok := databaseSizeBytes(labeled, "app", true); ok {
		t.Fatalf("size = %d; a figure without the documents must not be reported", size)
	}
}
