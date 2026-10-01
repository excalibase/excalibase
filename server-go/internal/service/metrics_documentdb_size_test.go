package service

import (
	"context"
	"strings"
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
	mock.ExecOutput["org-size-db/size-db-postgres-1"] = "HTTP/1.0 200 OK\r\n\r\n" + `cnpg_backends_total{state="active",datname="app"} 1
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

// The tenant image is CNPG's standard image: bash, no python. The exporter
// is read over bash's /dev/tcp and its HTTP status is checked.
func TestMetricsAreReadWithoutPython(t *testing.T) {
	dir := t.TempDir()
	store, _ := storage.NewFileSystemStore(dir)
	mock := k8s.NewMockClient()
	if err := store.Create(&domain.DatabaseInstance{
		ProjectID: "size-db", Namespace: "org-size-db", Status: "ACTIVE", DBType: domain.PostgreSQL, Tier: domain.Free, DatabaseName: "app",
	}); err != nil {
		t.Fatal(err)
	}
	mock.SetupPostgreSQLMock("size-db", "org-size-db", 1)
	mock.ExecOutput["org-size-db/size-db-postgres-1"] = "HTTP/1.0 200 OK\r\nContent-Type: text/plain\r\n\r\n" +
		"cnpg_pg_database_size_bytes{datname=\"app\"} 8388608\n"
	metrics, err := NewMetricsService(store, mock, dir).GetCurrentMetrics(context.Background(), "size-db")
	if err != nil {
		t.Fatal(err)
	}
	if !metrics.MetricsAvailable || metrics.DatabaseSizeBytes == nil || *metrics.DatabaseSizeBytes != appBytes {
		t.Fatalf("metrics = %+v, reason %v", metrics, metrics.UnavailableReason)
	}
	for _, command := range mock.ExecCommands {
		if strings.Contains(command, "python") {
			t.Fatalf("metrics fetched with python: %q", command)
		}
	}
}

func TestAnExporterErrorMakesTheMetricsUnavailable(t *testing.T) {
	if _, err := exporterBody("HTTP/1.0 500 Internal Server Error\r\n\r\nboom"); err == nil {
		t.Fatal("a 500 from the exporter must not be read as metrics")
	}
	if _, err := exporterBody("cnpg_up 1\n"); err == nil {
		t.Fatal("an answer without an HTTP status must not be read as metrics")
	}
}
