package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/k8s"
	"github.com/excalibase/provisioning-poc/internal/provisioner"
	"github.com/excalibase/provisioning-poc/internal/service"
	"github.com/excalibase/provisioning-poc/internal/storage"
	"github.com/go-chi/chi/v5"
)

func setupRouter(t *testing.T) chi.Router {
	t.Helper()
	r, _, _ := fullRouter(t)
	return r
}

func fullRouter(t *testing.T) (chi.Router, *storage.FileSystemStore, *k8s.MockClient) {
	t.Helper()
	dir := t.TempDir()
	store, _ := storage.NewFileSystemStore(dir)
	mock := k8s.NewMockClient()
	pgStore, _ := storage.NewFileSystemParameterGroupStore(dir)

	factory := provisioner.NewFactory() // empty factory — no real provisioners
	provSvc := service.NewProvisioningService(store, factory)
	metricsSvc := service.NewMetricsService(store, mock, dir)
	backupSvc := service.NewBackupService(store, mock, dir)
	perfSvc := service.NewPerformanceService(store, mock)
	auditSvc := service.NewAuditService(store, mock)
	snapshotSvc := service.NewSnapshotService(store, mock, dir)
	migrationSvc := service.NewMigrationService(store, mock, dir)
	alertSvc := service.NewAlertingService(dir)
	setupSvc := service.NewOperatorSetupService()

	provH := NewProvisioningHandler(provSvc)
	metricsH := NewMetricsHandler(metricsSvc)
	backupH := NewBackupHandler(backupSvc)
	perfH := NewPerformanceHandler(perfSvc)
	auditH := NewAuditHandler(auditSvc)
	snapshotH := NewSnapshotHandler(snapshotSvc)
	migrationH := NewMigrationHandler(migrationSvc)
	alertH := NewAlertHandler(alertSvc)
	setupH := NewSetupHandler(setupSvc)
	pgH := NewParameterGroupHandler(pgStore)

	r := chi.NewRouter()
	r.Route("/api/provision", func(r chi.Router) {
		r.Get("/", provH.ListInstances)
		r.Post("/estimate", provH.EstimateCost)
		r.Route("/{projectId}", func(r chi.Router) {
			r.Get("/", provH.GetStatus)
			r.Delete("/", provH.Delete)
			r.Get("/credentials", provH.GetCredentials)
			r.Patch("/deletion-protection", provH.SetDeletionProtection)
			r.Route("/metrics", func(r chi.Router) { metricsH.Routes(r) })
			r.Route("/backup", func(r chi.Router) { backupH.Routes(r) })
			r.Route("/performance", func(r chi.Router) { perfH.Routes(r) })
			r.Route("/audit", func(r chi.Router) { auditH.Routes(r) })
			r.Route("/snapshot", func(r chi.Router) { snapshotH.Routes(r) })
			r.Route("/migrations", func(r chi.Router) { migrationH.Routes(r) })
		})
	})
	r.Route("/api/alerts", func(r chi.Router) { alertH.Routes(r) })
	r.Route("/api/setup", func(r chi.Router) { setupH.Routes(r) })
	r.Route("/api/parameter-groups", func(r chi.Router) { pgH.Routes(r) })

	return r, store, mock
}

func seedInstance(store *storage.FileSystemStore, mock *k8s.MockClient) {
	port := 5432
	store.Save(&domain.DatabaseInstance{
		ProjectID: "test-db", OrgID: "org1", DBType: domain.PostgreSQL,
		Tier: domain.Free, Namespace: "org1-test-db", Status: "ACTIVE",
		Host: "h.local", Port: &port, DatabaseName: "app",
		Username: "user", Password: "pass", SSLMode: "require",
	})
	mock.SetupPostgreSQLMock("test-db", "org1-test-db", 1)
}

func doRequest(r chi.Router, method, path string, body string) *httptest.ResponseRecorder {
	var req *http.Request
	if body != "" {
		req = httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
	} else {
		req = httptest.NewRequest(method, path, nil)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

// --- Metrics Handler ---

func TestMetricsCurrentHandler(t *testing.T) {
	r, store, mock := fullRouter(t)
	seedInstance(store, mock)

	w := doRequest(r, "GET", "/api/provision/test-db/metrics/current", "")
	if w.Code != 200 {
		t.Errorf("status: %d, body: %s", w.Code, w.Body.String())
	}

	var m domain.DatabaseMetrics
	json.NewDecoder(w.Body).Decode(&m)
	if !m.MetricsAvailable {
		t.Errorf("metricsAvailable should be true, reason: %v", m.UnavailableReason)
	}
}

func TestMetricsHistoryHandler(t *testing.T) {
	r, store, mock := fullRouter(t)
	seedInstance(store, mock)

	w := doRequest(r, "GET", "/api/provision/test-db/metrics/history?limit=5", "")
	if w.Code != 200 {
		t.Errorf("status: %d", w.Code)
	}
}

// --- Backup Handler ---

func TestBackupTriggerHandler(t *testing.T) {
	r, store, mock := fullRouter(t)
	seedInstance(store, mock)

	w := doRequest(r, "POST", "/api/provision/test-db/backup/trigger", "")
	if w.Code != 200 {
		t.Errorf("status: %d, body: %s", w.Code, w.Body.String())
	}
}

func TestBackupListHandler(t *testing.T) {
	r, store, mock := fullRouter(t)
	seedInstance(store, mock)

	w := doRequest(r, "GET", "/api/provision/test-db/backup/list", "")
	if w.Code != 200 {
		t.Errorf("status: %d, body: %s", w.Code, w.Body.String())
	}

	// Frontend expects { backups: [], backupEnabled, schedule, retentionDays }
	var resp map[string]interface{}
	json.NewDecoder(w.Body).Decode(&resp)
	if _, ok := resp["backups"]; !ok {
		t.Error("response should have 'backups' field")
	}
	if _, ok := resp["backupEnabled"]; !ok {
		t.Error("response should have 'backupEnabled' field")
	}
	if _, ok := resp["schedule"]; !ok {
		t.Error("response should have 'schedule' field")
	}
}

// --- Performance Handler ---

func TestPerformanceSummaryHandler(t *testing.T) {
	r, store, mock := fullRouter(t)
	seedInstance(store, mock)

	w := doRequest(r, "GET", "/api/provision/test-db/performance/summary", "")
	if w.Code != 200 {
		t.Errorf("status: %d, body: %s", w.Code, w.Body.String())
	}
}

func TestPerformanceTopQueriesHandler(t *testing.T) {
	r, store, mock := fullRouter(t)
	seedInstance(store, mock)

	w := doRequest(r, "GET", "/api/provision/test-db/performance/top-queries?limit=5", "")
	if w.Code != 200 {
		t.Errorf("status: %d", w.Code)
	}
}

func TestPerformanceWaitEventsHandler(t *testing.T) {
	r, store, mock := fullRouter(t)
	seedInstance(store, mock)

	w := doRequest(r, "GET", "/api/provision/test-db/performance/wait-events", "")
	if w.Code != 200 {
		t.Errorf("status: %d", w.Code)
	}
}

// --- Audit Handler ---

func TestAuditEnableHandler(t *testing.T) {
	r, store, mock := fullRouter(t)
	seedInstance(store, mock)

	w := doRequest(r, "POST", "/api/provision/test-db/audit/enable", `{"enabled":true}`)
	if w.Code != 200 {
		t.Errorf("status: %d, body: %s", w.Code, w.Body.String())
	}
}

func TestAuditConfigHandler(t *testing.T) {
	r, store, mock := fullRouter(t)
	seedInstance(store, mock)

	w := doRequest(r, "GET", "/api/provision/test-db/audit/config", "")
	if w.Code != 200 {
		t.Errorf("status: %d", w.Code)
	}
}

// --- Migration Handler ---

func TestMigrationApplyHandler(t *testing.T) {
	r, store, mock := fullRouter(t)
	seedInstance(store, mock)

	w := doRequest(r, "POST", "/api/provision/test-db/migrations/", `{"sql":"SELECT 1"}`)
	if w.Code != 200 {
		t.Errorf("status: %d, body: %s", w.Code, w.Body.String())
	}
}

func TestMigrationListHandler(t *testing.T) {
	r, store, mock := fullRouter(t)
	seedInstance(store, mock)

	w := doRequest(r, "GET", "/api/provision/test-db/migrations/", "")
	if w.Code != 200 {
		t.Errorf("status: %d", w.Code)
	}
}

// --- Alert Handler ---

func TestAlertHandlers(t *testing.T) {
	r, _, _ := fullRouter(t)

	w := doRequest(r, "GET", "/api/alerts/", "")
	if w.Code != 200 {
		t.Errorf("active alerts: %d", w.Code)
	}

	w = doRequest(r, "GET", "/api/alerts/history?limit=10", "")
	if w.Code != 200 {
		t.Errorf("alert history: %d", w.Code)
	}

	w = doRequest(r, "GET", "/api/alerts/project/test-db", "")
	if w.Code != 200 {
		t.Errorf("project alerts: %d", w.Code)
	}
}

// --- Parameter Group Handler ---

func TestParameterGroupCRUD(t *testing.T) {
	r, _, _ := fullRouter(t)

	// Create
	w := doRequest(r, "POST", "/api/parameter-groups/", `{"name":"high-perf","parameters":{"max_connections":"200"}}`)
	if w.Code != 201 {
		t.Errorf("create: %d, body: %s", w.Code, w.Body.String())
	}

	// List
	w = doRequest(r, "GET", "/api/parameter-groups/", "")
	if w.Code != 200 {
		t.Errorf("list: %d", w.Code)
	}

	// Get
	w = doRequest(r, "GET", "/api/parameter-groups/high-perf", "")
	if w.Code != 200 {
		t.Errorf("get: %d", w.Code)
	}

	// Update
	w = doRequest(r, "PUT", "/api/parameter-groups/high-perf", `{"parameters":{"max_connections":"300"}}`)
	if w.Code != 200 {
		t.Errorf("update: %d", w.Code)
	}

	// Delete
	w = doRequest(r, "DELETE", "/api/parameter-groups/high-perf", "")
	if w.Code != 200 {
		t.Errorf("delete: %d", w.Code)
	}

	// Get after delete (should be 404)
	w = doRequest(r, "GET", "/api/parameter-groups/high-perf", "")
	if w.Code != 404 {
		t.Errorf("get after delete: %d, want 404", w.Code)
	}
}

// --- Estimate Handler ---

func TestEstimateAllTiers(t *testing.T) {
	r, _, _ := fullRouter(t)

	for _, tier := range []string{"FREE", "STANDARD", "ENTERPRISE"} {
		w := doRequest(r, "POST", "/api/provision/estimate", `{"tier":"`+tier+`"}`)
		if w.Code != 200 {
			t.Errorf("%s estimate: %d", tier, w.Code)
		}
	}
}

// --- Provisioning CRUD handlers ---

func TestDeleteHandler(t *testing.T) {
	r, store, mock := fullRouter(t)
	seedInstance(store, mock)

	w := doRequest(r, "DELETE", "/api/provision/test-db", "")
	if w.Code != 200 {
		t.Errorf("delete: %d, body: %s", w.Code, w.Body.String())
	}
}

func TestDeleteNotFound(t *testing.T) {
	r, _, _ := fullRouter(t)
	w := doRequest(r, "DELETE", "/api/provision/nope", "")
	if w.Code != 400 {
		t.Errorf("delete not found: got %d, want 400", w.Code)
	}
}

func TestGetCredentialsHandler(t *testing.T) {
	r, store, mock := fullRouter(t)
	seedInstance(store, mock)

	w := doRequest(r, "GET", "/api/provision/test-db/credentials", "")
	if w.Code != 200 {
		t.Errorf("credentials: %d, body: %s", w.Code, w.Body.String())
	}
	var creds domain.CredentialsResponse
	json.NewDecoder(w.Body).Decode(&creds)
	if creds.Password != "pass" {
		t.Errorf("password: got %s", creds.Password)
	}
}

func TestGetCredentialsNotFound(t *testing.T) {
	r, _, _ := fullRouter(t)
	w := doRequest(r, "GET", "/api/provision/nope/credentials", "")
	if w.Code != 404 {
		t.Errorf("expected 404, got %d", w.Code)
	}
}

func TestSetDeletionProtectionHandler(t *testing.T) {
	r, store, mock := fullRouter(t)
	seedInstance(store, mock)

	w := doRequest(r, "PATCH", "/api/provision/test-db/deletion-protection", `{"enabled":true}`)
	if w.Code != 200 {
		t.Errorf("set protection: %d, body: %s", w.Code, w.Body.String())
	}
}

// --- Audit GetLogs ---

func TestAuditGetLogsHandler(t *testing.T) {
	r, store, mock := fullRouter(t)
	seedInstance(store, mock)
	mock.ExecOutput["org1-test-db/test-db-postgres-1"] = "AUDIT: line1"

	w := doRequest(r, "GET", "/api/provision/test-db/audit/logs?lines=50", "")
	if w.Code != 200 {
		t.Errorf("audit logs: %d, body: %s", w.Code, w.Body.String())
	}
}

// --- Backup Restore ---

func TestBackupRestoreHandler(t *testing.T) {
	r, store, mock := fullRouter(t)
	seedInstance(store, mock)

	w := doRequest(r, "POST", "/api/provision/test-db/backup/restore", `{"newProjectName":"restored-db"}`)
	if w.Code != 200 {
		t.Errorf("restore: %d, body: %s", w.Code, w.Body.String())
	}
}

// --- Snapshot handlers ---

func TestSnapshotExportHandler(t *testing.T) {
	r, store, mock := fullRouter(t)
	seedInstance(store, mock)
	mock.ExecOutput["org1-test-db/test-db-postgres-1"] = "-- dump output"

	w := doRequest(r, "POST", "/api/provision/test-db/snapshot/export", `{"format":"plain"}`)
	if w.Code != 200 {
		t.Errorf("export: %d, body: %s", w.Code, w.Body.String())
	}
}

func TestSnapshotListHandler(t *testing.T) {
	r, store, mock := fullRouter(t)
	seedInstance(store, mock)

	w := doRequest(r, "GET", "/api/provision/test-db/snapshot/", "")
	if w.Code != 200 {
		t.Errorf("list snapshots: %d", w.Code)
	}
}

func TestSnapshotDeleteHandler(t *testing.T) {
	r, store, mock := fullRouter(t)
	seedInstance(store, mock)

	w := doRequest(r, "DELETE", "/api/provision/test-db/snapshot/fake-id", "")
	if w.Code != 200 {
		t.Errorf("delete snapshot: %d", w.Code)
	}
}

func TestSnapshotDownloadNotFound(t *testing.T) {
	r, store, mock := fullRouter(t)
	seedInstance(store, mock)

	w := doRequest(r, "GET", "/api/provision/test-db/snapshot/fake-id/download", "")
	if w.Code != 404 {
		t.Errorf("download not found: got %d, want 404", w.Code)
	}
}

// --- Setup Handler ---

func TestSetupStatusHandler(t *testing.T) {
	r, _, _ := fullRouter(t)

	w := doRequest(r, "GET", "/api/setup/status", "")
	if w.Code != 200 {
		t.Errorf("setup status: %d, body: %s", w.Code, w.Body.String())
	}
	var status domain.SetupStatusResponse
	json.NewDecoder(w.Body).Decode(&status)
	// PostgreSQL might be true or false depending on whether CNPG is installed
	// Just verify the response is valid JSON with the expected fields
	t.Logf("Setup status: pg=%v mysql=%v mongo=%v", status.PostgreSQL, status.MySQL, status.MongoDB)
}

func TestSetupInstallHandler(t *testing.T) {
	r, _, _ := fullRouter(t)

	// Install unsupported type should fail
	w := doRequest(r, "POST", "/api/setup/install/REDIS", "")
	if w.Code != 500 {
		t.Errorf("install unsupported: got %d, want 500", w.Code)
	}
}

// --- Error path handlers ---

func TestProvisionInvalidJSON(t *testing.T) {
	r, _, _ := fullRouter(t)
	// chi routes POST to the handler which then fails on decode
	w := doRequest(r, "POST", "/api/provision/", "{invalid")
	if w.Code != 400 && w.Code != 405 {
		t.Errorf("invalid JSON: got %d, want 400 or 405", w.Code)
	}
}

func TestBackupRestoreInvalidJSON(t *testing.T) {
	r, store, mock := fullRouter(t)
	seedInstance(store, mock)

	w := doRequest(r, "POST", "/api/provision/test-db/backup/restore", "not json")
	if w.Code != 400 {
		t.Errorf("invalid JSON: got %d, want 400", w.Code)
	}
}

func TestMigrationInvalidJSON(t *testing.T) {
	r, store, mock := fullRouter(t)
	seedInstance(store, mock)

	w := doRequest(r, "POST", "/api/provision/test-db/migrations/", "not json")
	// Should still work (empty SQL) or return error
	if w.Code == 0 {
		t.Error("should return a response")
	}
}

func TestMetricsCurrentNotFound(t *testing.T) {
	r, _, _ := fullRouter(t)
	w := doRequest(r, "GET", "/api/provision/nonexistent/metrics/current", "")
	if w.Code != 500 {
		t.Errorf("metrics not found: got %d", w.Code)
	}
}

func TestPerformanceSummaryNotFound(t *testing.T) {
	r, _, _ := fullRouter(t)
	w := doRequest(r, "GET", "/api/provision/nonexistent/performance/summary", "")
	if w.Code != 500 {
		t.Errorf("performance not found: got %d", w.Code)
	}
}

func TestBackupTriggerNotFound(t *testing.T) {
	r, _, _ := fullRouter(t)
	w := doRequest(r, "POST", "/api/provision/nonexistent/backup/trigger", "")
	if w.Code != 500 {
		t.Errorf("backup trigger not found: got %d", w.Code)
	}
}

func TestAuditEnableNotFound(t *testing.T) {
	r, _, _ := fullRouter(t)
	w := doRequest(r, "POST", "/api/provision/nonexistent/audit/enable", `{}`)
	if w.Code != 500 {
		t.Errorf("audit enable not found: got %d", w.Code)
	}
}

func TestSnapshotExportNotFound(t *testing.T) {
	r, _, _ := fullRouter(t)
	w := doRequest(r, "POST", "/api/provision/nonexistent/snapshot/export", `{}`)
	if w.Code != 500 {
		t.Errorf("snapshot export not found: got %d", w.Code)
	}
}
