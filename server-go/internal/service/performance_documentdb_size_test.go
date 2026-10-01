package service

import (
	"context"
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/k8s"
	"github.com/excalibase/provisioning-poc/internal/storage"
)

// EXC-531: the performance summary's size is the customer's data — app, and
// for a DocumentDB project the postgres database its documents live in —
// not whatever database psql happens to connect to.
func sizeQueryFor(t *testing.T, documentDB bool) string {
	t.Helper()
	dir := t.TempDir()
	store, _ := storage.NewFileSystemStore(dir)
	mock := k8s.NewMockClient()
	if err := store.Create(&domain.DatabaseInstance{
		ProjectID: testPerfDB, Namespace: "org-perf-db", Status: "ACTIVE",
		DBType: domain.PostgreSQL, DocumentDB: documentDB, DatabaseName: "app",
	}); err != nil {
		t.Fatalf("create: %v", err)
	}
	mock.ExecOutput[testPerfDBPod] = "1"
	if _, err := NewPerformanceService(store, mock).GetSummary(context.Background(), testPerfDB); err != nil {
		t.Fatalf("GetSummary: %v", err)
	}
	for _, command := range mock.ExecCommands {
		if strings.Contains(command, "pg_database_size") {
			return command
		}
	}
	t.Fatalf("no size query among %v", mock.ExecCommands)
	return ""
}

func TestThePerformanceSizeOfAPostgresProjectIsItsAppDatabase(t *testing.T) {
	query := sizeQueryFor(t, false)
	if !strings.Contains(query, "pg_database_size("+sqlTextLiteral("app")+")") || strings.Contains(query, "'postgres'") {
		t.Fatalf("size query = %q, want the app database only", query)
	}
}

func TestThePerformanceSizeOfADocumentDBProjectCountsItsDocuments(t *testing.T) {
	query := sizeQueryFor(t, true)
	if !strings.Contains(query, "pg_database_size("+sqlTextLiteral("app")+")") || !strings.Contains(query, "pg_database_size('postgres')") {
		t.Fatalf("size query = %q, want app plus postgres", query)
	}
}
