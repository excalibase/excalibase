package service

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/k8s"
	"github.com/excalibase/provisioning-poc/internal/storage"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

const (
	testPerfDB    = "perf-db"
	testPerfDBPod = "org-perf-db/perf-db-postgres-1"
	// testPerfPrimary is where the cluster's primary is after a failover.
	testPerfPrimary = "org-perf-db/perf-db-postgres-2"
)

// statsAnswer is the statistics function's answer around queries.
func statsAnswer(queries string) string {
	return `{"queries":` + queries + `,"slowCount":0,"avgMeanMs":null}`
}

func setupPerfTest(t *testing.T) (*PerformanceService, *k8s.MockClient) {
	t.Helper()
	dir := t.TempDir()
	store, _ := storage.NewFileSystemStore(dir)
	mock := k8s.NewMockClient()
	mock.CRDs["org-perf-db/perf-db-postgres"] = &unstructured.Unstructured{Object: map[string]interface{}{
		"status": map[string]interface{}{"currentPrimary": "perf-db-postgres-2"}}}

	store.Create(&domain.DatabaseInstance{
		ProjectID: testPerfDB, Namespace: "org-perf-db", Status: "ACTIVE",
		DBType: domain.PostgreSQL, DatabaseName: "perfdb",
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
	mock.ExecOutput[testPerfPrimary] = statsAnswer(`[{"query":"SELECT $1","calls":100,"totalTimeMs":50.5,"meanTimeMs":0.5,"rows":100},{"query":"SELECT 2","calls":50,"totalTimeMs":25,"meanTimeMs":0.5,"rows":50}]`)

	queries, err := svc.GetTopQueries(context.Background(), testPerfDB, 10)
	if err != nil {
		t.Fatalf("GetTopQueries: %v", err)
	}
	if len(queries) != 2 {
		t.Errorf("expected 2 queries, got %d", len(queries))
	}
	if queries[0].Calls != 100 || queries[0].TotalExecTimeMs != 50.5 || queries[0].AvgExecTimeMs != 0.5 {
		t.Errorf("first query: %+v", queries[0])
	}
}

// EXC-654: once pg_stat_statements is on, the older top-queries read must not
// answer the platform's statements or another database's: it is the same
// filtered read as the slow-queries page, in the project database.
func TestGetTopQueriesReadsOnlyTheProjectsStatements(t *testing.T) {
	svc, mock := setupPerfTest(t)
	mock.ExecOutput[testPerfPrimary] = statsAnswer("[]")
	if _, err := svc.GetTopQueries(context.Background(), testPerfDB, 10); err != nil {
		t.Fatal(err)
	}
	cmd := mock.ExecCommands[len(mock.ExecCommands)-1]
	if !strings.Contains(cmd, "dbname='perfdb'") || !strings.Contains(cmd, projectStatementRoleFilter) {
		t.Errorf("top queries ran %s", cmd)
	}
}

// The read creates a temporary function, which a standby refuses: it runs on
// the cluster's current primary, never on a fixed pod.
func TestGetTopQueriesReadsTheCurrentPrimary(t *testing.T) {
	svc, mock := setupPerfTest(t)
	mock.ExecOutput[testPerfPrimary] = statsAnswer("[]")
	if _, err := svc.GetTopQueries(context.Background(), testPerfDB, 10); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(mock.Calls, "ExecInPod:"+testPerfPrimary) || slices.Contains(mock.Calls, "ExecInPod:"+testPerfDBPod) {
		t.Errorf("calls = %v", mock.Calls)
	}
}

func TestGetTopQueriesWithoutAPrimaryIsAnError(t *testing.T) {
	svc, mock := setupPerfTest(t)
	delete(mock.CRDs, "org-perf-db/perf-db-postgres")
	if _, err := svc.GetTopQueries(context.Background(), testPerfDB, 10); err == nil {
		t.Fatal("read statistics with no primary known")
	}
}

// The summary counts every statement of the project, not just the costliest page.
func TestGetSummaryCountsEveryStatement(t *testing.T) {
	svc, mock := setupPerfTest(t)
	mock.ExecOutput[testPerfDBPod] = "1"
	mock.ExecOutput[testPerfPrimary] = `{"queries":[],"slowCount":7,"avgMeanMs":12.5}`
	summary, err := svc.GetSummary(context.Background(), testPerfDB)
	if err != nil {
		t.Fatal(err)
	}
	if summary.SlowQueryCount == nil || *summary.SlowQueryCount != 7 || summary.AvgQueryTimeMs == nil || *summary.AvgQueryTimeMs != 12.5 {
		t.Errorf("summary = %+v", summary)
	}
}

func TestPerformanceNeverReadsTheStatisticsViewUnqualified(t *testing.T) {
	svc, mock := setupPerfTest(t)
	mock.ExecOutput[testPerfDBPod] = "1"
	_, _ = svc.GetSummary(context.Background(), testPerfDB)
	for _, cmd := range mock.ExecCommands {
		if strings.Contains(cmd, "FROM pg_stat_statements") {
			t.Errorf("superuser read an unqualified view a customer could have planted: %s", cmd)
		}
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
