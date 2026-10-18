package service

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/k8s"
)

// CNPG's /controller/log/postgres.csv is a named pipe its log collector reads:
// a command run on it blocks and takes lines from the collector. Every log
// read goes through the pod's log instead.
func assertNoExecIntoThePod(t *testing.T, mock *k8s.MockClient) {
	t.Helper()
	for _, call := range mock.Calls {
		if strings.HasPrefix(call, "ExecInPod:") {
			t.Fatalf("the log read ran a command in the pod: %v", mock.ExecCommands)
		}
	}
}

func TestDatabaseLogsWithoutLokiReadThePodLog(t *testing.T) {
	svc, _, mock := setupOpsTest(t)
	mock.PodLogs[testOpsDBNS+"/ops-db-postgres-1"] = "line one\nline two\n"

	logs, err := svc.GetLogs(context.Background(), testOpsDB, 50)
	if err != nil {
		t.Fatalf("GetLogs: %v", err)
	}
	if logs != "line one\nline two\n" {
		t.Fatalf("logs = %q", logs)
	}
	assertNoExecIntoThePod(t, mock)
}

func TestDatabaseLogsFallBackToThePodLogWhenLokiDoesNotAnswer(t *testing.T) {
	svc, _, mock := setupOpsTest(t)
	mock.PodLogs[testOpsDBNS+"/ops-db-postgres-1"] = "from the pod\n"
	closed := httptest.NewServer(http.NotFoundHandler())
	closed.Close()
	svc.SetLokiURL(closed.URL)

	logs, err := svc.GetLogs(context.Background(), testOpsDB, 50)
	if err != nil {
		t.Fatalf("GetLogs: %v", err)
	}
	if logs != "from the pod\n" {
		t.Fatalf("logs = %q", logs)
	}
}

func TestDatabaseLogsUnavailableWhenThePodLogCannotBeRead(t *testing.T) {
	svc, _, _ := setupOpsTest(t)
	if _, err := svc.GetLogs(context.Background(), testOpsDB, 50); err != ErrLogsUnavailable {
		t.Fatalf("err = %v, want ErrLogsUnavailable", err)
	}
}

func TestAuditLogsAreThePgauditLinesOfThePodLog(t *testing.T) {
	svc, mock := setupAuditTest(t)
	mock.PodLogs["org-aud-db/aud-db-postgres-1"] = strings.Join([]string{
		`{"level":"info","logger":"postgres","msg":"record","record":{"message":"checkpoint complete"}}`,
		`{"level":"info","logger":"pgaudit","msg":"record","record":{"audit":{"audit_type":"SESSION","command":"SELECT"}}}`,
		`{"level":"info","logger":"postgres","msg":"record","record":{"message":"AUDIT: SESSION,1,1,READ,SELECT,,,SELECT 1,<not logged>"}}`,
		`{"level":"info","logger":"postgres","msg":"record","record":{"message":"connection received"}}`,
	}, "\n")

	logs, err := svc.GetAuditLogs(context.Background(), testAudDB, 100)
	if err != nil {
		t.Fatalf("GetAuditLogs: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(logs), "\n")
	if len(lines) != 2 || !strings.Contains(lines[0], `"logger":"pgaudit"`) || !strings.Contains(lines[1], "AUDIT:") {
		t.Fatalf("audit lines = %q", lines)
	}
	assertNoExecIntoThePod(t, mock)
}

func TestAuditLogsKeepTheLastLines(t *testing.T) {
	svc, mock := setupAuditTest(t)
	mock.PodLogs["org-aud-db/aud-db-postgres-1"] = "AUDIT: one\nAUDIT: two\nAUDIT: three\n"

	logs, err := svc.GetAuditLogs(context.Background(), testAudDB, 2)
	if err != nil {
		t.Fatalf("GetAuditLogs: %v", err)
	}
	if logs != "AUDIT: two\nAUDIT: three\n" {
		t.Fatalf("logs = %q", logs)
	}
}

// A project namespace also runs the project's apps, functions and watcher;
// the database's usage is its postgres pods alone.
func TestDatabaseUsageCountsOnlyThePostgresPods(t *testing.T) {
	svc, mock := setupMetricsCollectTest(t)
	mock.Metrics["org-m-db"] = []k8s.PodResourceMetrics{
		{Name: "m-db-postgres-1", CPUMillis: 100, MemoryMB: 128},
		{Name: "app-web-7c9d-x2", CPUMillis: 400, MemoryMB: 300},
		{Name: "deno-runtime-5f6-abc", CPUMillis: 300, MemoryMB: 200},
	}

	m, err := svc.GetCurrentMetrics(context.Background(), "m-db")
	if err != nil {
		t.Fatalf("GetCurrentMetrics: %v", err)
	}
	if m.CPUUsageCores == nil || *m.CPUUsageCores != 0.1 {
		t.Fatalf("cpu cores = %v, want 0.1", m.CPUUsageCores)
	}
	if m.MemoryUsageMB == nil || *m.MemoryUsageMB != 128 {
		t.Fatalf("memory = %v, want 128", m.MemoryUsageMB)
	}
	if len(m.Pods) != 1 || m.Pods[0].Name != "m-db-postgres-1" {
		t.Fatalf("pods = %+v", m.Pods)
	}
}

// Without metrics-server's figures the database's own figures still arrive,
// and CPU and memory stay unset for Studio to name as not available.
func TestDatabaseUsageIsUnsetWhenMetricsServerDoesNotAnswer(t *testing.T) {
	svc, mock := setupMetricsCollectTest(t)
	delete(mock.Metrics, "org-m-db")

	m, err := svc.GetCurrentMetrics(context.Background(), "m-db")
	if err != nil {
		t.Fatalf("GetCurrentMetrics: %v", err)
	}
	if !m.MetricsAvailable || m.ActiveConnections == nil {
		t.Fatalf("the database's figures are missing: %+v", m)
	}
	if m.CPUUsagePercent != nil || m.MemoryUsagePercent != nil || m.Pods != nil {
		t.Fatalf("usage set without metrics-server: %+v", m)
	}
}

func TestDatabaseUsageIsUnsetWhenNoPostgresPodIsSampled(t *testing.T) {
	svc, mock := setupMetricsCollectTest(t)
	mock.Metrics["org-m-db"] = []k8s.PodResourceMetrics{{Name: "deno-runtime-5f6-abc", CPUMillis: 300, MemoryMB: 200}}

	m, _ := svc.GetCurrentMetrics(context.Background(), "m-db")
	if m.CPUUsageCores != nil {
		t.Fatalf("cpu cores = %v, want unset", *m.CPUUsageCores)
	}
}

func TestAuditLogsAreEmptyWhenNothingWasAudited(t *testing.T) {
	svc, mock := setupAuditTest(t)
	mock.PodLogs["org-aud-db/aud-db-postgres-1"] = `{"logger":"postgres","msg":"record"}` + "\n"

	logs, err := svc.GetAuditLogs(context.Background(), testAudDB, 100)
	if err != nil || logs != "" {
		t.Fatalf("logs = %q, err = %v", logs, err)
	}
}

func TestAuditLogsFailWhenThePodLogCannotBeRead(t *testing.T) {
	svc, _ := setupAuditTest(t)
	if _, err := svc.GetAuditLogs(context.Background(), testAudDB, 100); err == nil {
		t.Fatal("expected an error")
	}
}
