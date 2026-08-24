package service

import (
	"context"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/k8s"
	"github.com/excalibase/provisioning-poc/internal/storage"
)

const (
	testPerfDB    = "perf-db"
	testPerfDBPod = "org-perf-db/perf-db-postgres-1"
)

func setupPerfTest(t *testing.T) (*PerformanceService, *k8s.MockClient) {
	t.Helper()
	dir := t.TempDir()
	store, _ := storage.NewFileSystemStore(dir)
	mock := k8s.NewMockClient()

	store.Save(&domain.DatabaseInstance{
		ProjectID: testPerfDB, Namespace: "org-perf-db", Status: "ACTIVE",
		DBType: domain.PostgreSQL,
	})

	return NewPerformanceService(store, mock), mock
}

func TestGetSummaryPgStatStatementsEnabled(t *testing.T) {
	svc, mock := setupPerfTest(t)
	pod := testPerfDBPod
	// Mock pg_stat_statements check
	mock.ExecOutput[pod] = "1\n3\n9\n99.5\n7548 kB\n0\n1.5"

	summary, err := svc.GetSummary(context.Background(), testPerfDB)
	if err != nil {
		t.Fatalf("GetSummary: %v", err)
	}
	if !summary.Available {
		t.Error("should be available")
	}
}

func TestGetSummaryNotFound(t *testing.T) {
	dir := t.TempDir()
	store, _ := storage.NewFileSystemStore(dir)
	mock := k8s.NewMockClient()
	svc := NewPerformanceService(store, mock)

	_, err := svc.GetSummary(context.Background(), "nonexistent")
	if err == nil {
		t.Error("expected error for nonexistent project")
	}
}

func TestGetTopQueries(t *testing.T) {
	svc, mock := setupPerfTest(t)
	mock.ExecOutput[testPerfDBPod] = "SELECT 1|100|50.5|0.5|0.1|1.0|100\nSELECT 2|50|25.0|0.5|0.1|1.0|50"

	queries, err := svc.GetTopQueries(context.Background(), testPerfDB, 10)
	if err != nil {
		t.Fatalf("GetTopQueries: %v", err)
	}
	if len(queries) != 2 {
		t.Errorf("expected 2 queries, got %d", len(queries))
	}
	if queries[0].Calls != 100 {
		t.Errorf("first query calls: got %d", queries[0].Calls)
	}
}

func TestGetWaitEvents(t *testing.T) {
	svc, mock := setupPerfTest(t)
	mock.ExecOutput[testPerfDBPod] = "IO|DataFileRead|5\nLock|relation|2"

	events, err := svc.GetWaitEvents(context.Background(), testPerfDB)
	if err != nil {
		t.Fatalf("GetWaitEvents: %v", err)
	}
	if len(events) != 2 {
		t.Errorf("expected 2 events, got %d", len(events))
	}
	if events[0].Count != 5 {
		t.Errorf("first event count: got %d", events[0].Count)
	}
}
