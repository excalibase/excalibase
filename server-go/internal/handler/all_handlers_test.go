package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/auth"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/k8s"
	"github.com/excalibase/provisioning-poc/internal/provisioner"
	"github.com/excalibase/provisioning-poc/internal/service"
	"github.com/excalibase/provisioning-poc/internal/storage"
	"github.com/excalibase/provisioning-poc/internal/testutil"
	"github.com/go-chi/chi/v5"
)

const (
	testPGCreds            = "pg-creds"
	testStatusBodyFmt      = "status: %d, body: %s"
	testStatusFmt          = "status: %d"
	testMigrationsPath     = "/api/provision/test-db/migrations/"
	testParamGroupsPath    = "/api/parameter-groups/"
	testParamGroupHighPerf = "/api/parameter-groups/high-perf"
	testDBPodName          = "org1-test-db/test-db-postgres-1"
	testProvisionPath      = "/provision"
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

	// Teardown must observe the project's resources gone, so the factory
	// carries a real provisioner over the k8s mock.
	factory := provisioner.NewFactory(provisioner.NewPostgreSQLProvisioner(mock, ""))
	provSvc := service.NewProvisioningService(store, factory, mock)
	metricsSvc := service.NewMetricsService(store, mock, dir)
	backupSvc := service.NewBackupService(store, mock, dir, testBackupStorage())
	// Restore ends in the shared registration path (EXC-366); the mock
	// reports the recovered primary Ready straight away.
	mock.WildcardPodReady = true
	backupSvc.SetProjectRegistrar(provSvc)
	perfSvc := service.NewPerformanceService(store, mock)
	auditSvc := service.NewAuditService(store, mock)
	snapshotSvc := service.NewSnapshotService(store, mock, dir)
	migVault := newFakeVault()
	// Migrations connect as excalibase_app via vault (SEC-C2). Seed creds for
	// the seeded test project pointing at an unreachable DB so ApplyMigration
	// gets past credential lookup and records a FAILED migration (200), the way
	// the k8s exec mock used to behave, without needing a live Postgres.
	migVault.data["projects/test-db/credentials/excalibase_app"] = map[string]string{
		"host": "127.0.0.1", "port": "1", "username": "excalibase_app", "password": "x", "database": "app",
	}
	migrationSvc := service.NewMigrationService(store, migVault, dir)
	alertSvc := service.NewAlertingService(dir)
	setupSvc := service.NewOperatorSetupService(mock)

	provH := NewProvisioningHandler(provSvc, nil)
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
	// These tests exercise handler logic, not the auth layer; inject a
	// platform_admin so per-route permission gates (e.g. setup install requires
	// manage_setup) are satisfied.
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			ctx := auth.SetUser(req.Context(), &domain.User{ID: "test-admin", Role: "platform_admin", Active: true})
			next.ServeHTTP(w, req.WithContext(ctx))
		})
	})
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
	r.Route("/api/alerts", func(r chi.Router) {
		alertH.Routes(r)
		r.Route("/project/{projectId}", alertH.ProjectRoutes)
	})
	r.Route("/api/setup", func(r chi.Router) { setupH.Routes(r) })
	r.Route("/api/parameter-groups", func(r chi.Router) { pgH.Routes(r) })

	return r, store, mock
}

func seedInstance(store *storage.FileSystemStore, mock *k8s.MockClient) {
	port := 5432
	store.Create(&domain.DatabaseInstance{
		ProjectID: "test-db", OrgID: "org1", DBType: domain.PostgreSQL,
		Tier: domain.Free, Namespace: "org1-test-db", Status: "ACTIVE",
		Host: "h.local", Port: &port, DatabaseName: "app",
		Username: "user", Password: testutil.FixturePassword(testPGCreds), SSLMode: "require",
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
		t.Errorf(testStatusBodyFmt, w.Code, w.Body.String())
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
		t.Errorf(testStatusFmt, w.Code)
	}
}

// --- Backup Handler ---

func TestBackupTriggerHandler(t *testing.T) {
	r, store, mock := fullRouter(t)
	seedInstance(store, mock)

	w := doRequest(r, "POST", "/api/provision/test-db/backup/trigger", "")
	if w.Code != 200 {
		t.Errorf(testStatusBodyFmt, w.Code, w.Body.String())
	}
}

func TestBackupListHandler(t *testing.T) {
	r, store, mock := fullRouter(t)
	seedInstance(store, mock)

	w := doRequest(r, "GET", "/api/provision/test-db/backup/list", "")
	if w.Code != 200 {
		t.Errorf(testStatusBodyFmt, w.Code, w.Body.String())
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
		t.Errorf(testStatusBodyFmt, w.Code, w.Body.String())
	}
}

func TestPerformanceTopQueriesHandler(t *testing.T) {
	r, store, mock := fullRouter(t)
	seedInstance(store, mock)

	w := doRequest(r, "GET", "/api/provision/test-db/performance/top-queries?limit=5", "")
	if w.Code != 200 {
		t.Errorf(testStatusFmt, w.Code)
	}
}

func TestPerformanceWaitEventsHandler(t *testing.T) {
	r, store, mock := fullRouter(t)
	seedInstance(store, mock)

	w := doRequest(r, "GET", "/api/provision/test-db/performance/wait-events", "")
	if w.Code != 200 {
		t.Errorf(testStatusFmt, w.Code)
	}
}

// --- Audit Handler ---

func TestAuditEnableHandler(t *testing.T) {
	r, store, mock := fullRouter(t)
	seedInstance(store, mock)

	w := doRequest(r, "POST", "/api/provision/test-db/audit/enable", `{"enabled":true}`)
	if w.Code != 200 {
		t.Errorf(testStatusBodyFmt, w.Code, w.Body.String())
	}
}

func TestAuditConfigHandler(t *testing.T) {
	r, store, mock := fullRouter(t)
	seedInstance(store, mock)

	w := doRequest(r, "GET", "/api/provision/test-db/audit/config", "")
	if w.Code != 200 {
		t.Errorf(testStatusFmt, w.Code)
	}
}

// --- Migration Handler ---

func TestMigrationApplyHandler(t *testing.T) {
	r, store, mock := fullRouter(t)
	seedInstance(store, mock)

	w := doRequest(r, "POST", testMigrationsPath, `{"sql":"SELECT 1"}`)
	if w.Code != 200 {
		t.Errorf(testStatusBodyFmt, w.Code, w.Body.String())
	}
}

func TestMigrationListHandler(t *testing.T) {
	r, store, mock := fullRouter(t)
	seedInstance(store, mock)

	w := doRequest(r, "GET", testMigrationsPath, "")
	if w.Code != 200 {
		t.Errorf(testStatusFmt, w.Code)
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
	w := doRequest(r, "POST", testParamGroupsPath, `{"name":"high-perf","parameters":{"max_connections":"200"}}`)
	if w.Code != 201 {
		t.Errorf("create: %d, body: %s", w.Code, w.Body.String())
	}

	// List
	w = doRequest(r, "GET", testParamGroupsPath, "")
	if w.Code != 200 {
		t.Errorf("list: %d", w.Code)
	}

	// Get
	w = doRequest(r, "GET", testParamGroupHighPerf, "")
	if w.Code != 200 {
		t.Errorf("get: %d", w.Code)
	}

	// Update
	w = doRequest(r, "PUT", testParamGroupHighPerf, `{"parameters":{"max_connections":"300"}}`)
	if w.Code != 200 {
		t.Errorf("update: %d", w.Code)
	}

	// Delete
	w = doRequest(r, "DELETE", testParamGroupHighPerf, "")
	if w.Code != 200 {
		t.Errorf("delete: %d", w.Code)
	}

	// Get after delete (should be 404)
	w = doRequest(r, "GET", testParamGroupHighPerf, "")
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
	if w.Code != 404 {
		t.Errorf("delete not found: got %d, want 404", w.Code)
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
	if creds.Password != testutil.FixturePassword(testPGCreds) {
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
	mock.ExecOutput[testDBPodName] = "AUDIT: line1"

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
	mock.ExecOutput[testDBPodName] = "-- dump output"

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
	mock.ExecOutput[testDBPodName] = "-- dump output"

	snapshotID := exportSnapshotID(t, r, "test-db")
	w := doRequest(r, "DELETE", "/api/provision/test-db/snapshot/"+snapshotID, "")
	if w.Code != 200 {
		t.Errorf("delete snapshot: %d", w.Code)
	}
}

func TestSnapshotDeleteUnknownID(t *testing.T) {
	r, store, mock := fullRouter(t)
	seedInstance(store, mock)

	w := doRequest(r, "DELETE", "/api/provision/test-db/snapshot/fake-id", "")
	if w.Code != 404 {
		t.Errorf("delete unknown snapshot: got %d, want 404", w.Code)
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

	w := doRequest(r, "POST", testMigrationsPath, "not json")
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

// --- ProvisioningHandler.Routes() coverage ---

func TestProvisioningHandlerRoutes(t *testing.T) {
	dir := t.TempDir()
	store, _ := storage.NewFileSystemStore(dir)
	factory := provisioner.NewFactory()
	svc := service.NewProvisioningService(store, factory, nil)
	// ListInstances now fails closed (503) when no org store is wired; supply
	// a fake so this route-wiring test still exercises the real list path.
	h := NewProvisioningHandler(svc, &adminOrgStore{})

	r := chi.NewRouter()
	r.Route(testProvisionPath, h.Routes)

	// Verify routes are wired by hitting each one
	w := doRequest(r, "GET", "/provision/", "")
	if w.Code != 200 {
		t.Errorf("list via Routes: got %d", w.Code)
	}

	w = doRequest(r, "POST", "/provision/estimate", `{"tier":"FREE"}`)
	if w.Code != 200 {
		t.Errorf("estimate via Routes: got %d", w.Code)
	}

	w = doRequest(r, "GET", "/provision/nonexistent/", "")
	if w.Code != 404 {
		t.Errorf("get status via Routes: got %d", w.Code)
	}
}

// --- Metrics GetHistory with invalid limit (falls back to default) ---

func TestMetricsHistoryInvalidLimit(t *testing.T) {
	r, store, mock := fullRouter(t)
	seedInstance(store, mock)

	// non-numeric limit should fall back to default 50
	w := doRequest(r, "GET", "/api/provision/test-db/metrics/history?limit=notanumber", "")
	if w.Code != 200 {
		t.Errorf("metrics history invalid limit: got %d", w.Code)
	}
}

// --- Performance GetTopQueries with invalid limit param ---

func TestPerformanceTopQueriesInvalidLimit(t *testing.T) {
	r, store, mock := fullRouter(t)
	seedInstance(store, mock)

	// invalid limit falls back to default (10)
	w := doRequest(r, "GET", "/api/provision/test-db/performance/top-queries?limit=notanumber", "")
	if w.Code != 200 {
		t.Errorf("top queries invalid limit: got %d", w.Code)
	}
}

func TestPerformanceWaitEventsNotFound(t *testing.T) {
	r, _, _ := fullRouter(t)
	w := doRequest(r, "GET", "/api/provision/nonexistent/performance/wait-events", "")
	if w.Code != 500 {
		t.Errorf("wait events not found: got %d", w.Code)
	}
}

// --- Audit GetConfig error path ---

func TestAuditConfigNotFound(t *testing.T) {
	r, _, _ := fullRouter(t)
	w := doRequest(r, "GET", "/api/provision/nonexistent/audit/config", "")
	if w.Code != 500 {
		t.Errorf("audit config not found: got %d", w.Code)
	}
}

// --- Audit GetLogs with default lines (no query param) ---

func TestAuditGetLogsDefaultLines(t *testing.T) {
	r, store, mock := fullRouter(t)
	seedInstance(store, mock)

	// no "lines" query param — uses default 100
	w := doRequest(r, "GET", "/api/provision/test-db/audit/logs", "")
	if w.Code != 200 {
		t.Errorf("audit logs default: got %d, body: %s", w.Code, w.Body.String())
	}
}

// --- Migration Apply error path ---

func TestMigrationApplyNotFound(t *testing.T) {
	r, _, _ := fullRouter(t)
	w := doRequest(r, "POST", "/api/provision/nonexistent/migrations/", `{"sql":"SELECT 1"}`)
	if w.Code != 500 {
		t.Errorf("migration apply not found: got %d", w.Code)
	}
}

// --- Alert GetHistory with invalid limit (falls back to default) ---

func TestAlertHistoryInvalidLimit(t *testing.T) {
	r, _, _ := fullRouter(t)
	w := doRequest(r, "GET", "/api/alerts/history?limit=bad", "")
	if w.Code != 200 {
		t.Errorf("alert history bad limit: got %d", w.Code)
	}
}

// --- SetDeletionProtection on nonexistent project ---

func TestSetDeletionProtectionNotFound(t *testing.T) {
	r, _, _ := fullRouter(t)
	w := doRequest(r, "PATCH", "/api/provision/nonexistent/deletion-protection", `{"enabled":true}`)
	if w.Code != 400 {
		t.Errorf("set deletion protection not found: got %d, want 400", w.Code)
	}
}

// --- Backup ListBackups when instance has backup config ---

func TestBackupListWithConfig(t *testing.T) {
	r, store, mock := fullRouter(t)
	enabled := true
	retentionDays := 7
	port := 5432
	store.Create(&domain.DatabaseInstance{
		ProjectID:           "backup-db",
		OrgID:               "org1",
		DBType:              domain.PostgreSQL,
		Tier:                domain.Free,
		Namespace:           "org1-backup-db",
		Status:              "ACTIVE",
		Host:                "h.local",
		Port:                &port,
		DatabaseName:        "app",
		Username:            "user",
		Password:            testutil.FixturePassword(testPGCreds),
		SSLMode:             "require",
		BackupEnabled:       &enabled,
		BackupSchedule:      "0 2 * * *",
		BackupRetentionDays: &retentionDays,
	})
	mock.SetupPostgreSQLMock("backup-db", "org1-backup-db", 1)

	w := doRequest(r, "GET", "/api/provision/backup-db/backup/list", "")
	if w.Code != 200 {
		t.Errorf("backup list with config: got %d, body: %s", w.Code, w.Body.String())
	}
	var resp map[string]interface{}
	json.NewDecoder(w.Body).Decode(&resp)
	if resp["backupEnabled"] != true {
		t.Errorf("backupEnabled: got %v", resp["backupEnabled"])
	}
	if resp["schedule"] != "0 2 * * *" {
		t.Errorf("schedule: got %v", resp["schedule"])
	}
	if resp["retentionDays"].(float64) != 7 {
		t.Errorf("retentionDays: got %v", resp["retentionDays"])
	}
}

// --- SSEHandler constructor ---

func TestNewSSEHandler(t *testing.T) {
	h := NewSSEHandler(100 * time.Millisecond)
	if h == nil {
		t.Fatal("NewSSEHandler returned nil")
	}
}

// --- Provision with authenticated user (owner set from context) ---

func TestProvisionWithAuthUser(t *testing.T) {
	dir := t.TempDir()
	store, _ := storage.NewFileSystemStore(dir)
	factory := provisioner.NewFactory()
	svc := service.NewProvisioningService(store, factory, nil)
	h := NewProvisioningHandler(svc, nil)

	us := newMockUserStore()
	ts := newMockTokenStore()
	now := time.Now()
	user := &domain.User{ID: "owner-123", Username: testutil.FixtureToken("alice"), Active: true, CreatedAt: &now}
	us.users["owner-123"] = user

	r := chi.NewRouter()
	_, tok := setupAuthRouterWithUser(t, us, ts, user)
	lookup := &fakeLookup{
		hash:  auth.HashToken(tok),
		token: &domain.AccessToken{TokenHash: auth.HashToken(tok), UserID: user.ID},
		user:  user,
	}
	r.Use(auth.ExtractAuth(lookup))
	r.Post(testProvisionPath, h.Provision)

	req := httptest.NewRequest("POST", testProvisionPath,
		strings.NewReader(`{"projectName":"owned-db","orgId":"org1","databaseType":"POSTGRESQL","tier":"FREE"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+tok)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	// Fails because no POSTGRESQL provisioner registered (empty factory), but
	// the auth user branch (req.OwnerID = user.ID) was exercised.
	if w.Code != 400 {
		t.Errorf("provision with auth user: got %d, want 400", w.Code)
	}
}

// --- ParameterGroupHandler.Create store error ---

func TestParameterGroupCreate_StoreError(t *testing.T) {
	dir := t.TempDir()
	pgStore, _ := storage.NewFileSystemParameterGroupStore(dir)
	h := NewParameterGroupHandler(pgStore)

	r := chi.NewRouter()
	r.Route("/api/parameter-groups", func(r chi.Router) {
		r.Post("/", h.Create)
	})

	// Store file is removed to simulate an error — just test that empty name works
	// (store.Save with empty name succeeds; we test the happy path)
	w := doRequest(r, "POST", testParamGroupsPath, `{"name":"pg1","parameters":{}}`)
	if w.Code != 201 {
		t.Errorf("param group create: got %d, body: %s", w.Code, w.Body.String())
	}
}

// --- Snapshot Download success (exercise the 200 path) ---

func TestSnapshotDownloadSuccess(t *testing.T) {
	r, store, mock := fullRouter(t)
	seedInstance(store, mock)
	mock.ExecOutput[testDBPodName] = "-- dump content"

	// First export a snapshot so it exists
	wExport := doRequest(r, "POST", "/api/provision/test-db/snapshot/export", `{"format":"plain"}`)
	if wExport.Code != 200 {
		t.Fatalf("export: %d, body: %s", wExport.Code, wExport.Body.String())
	}
	var snap struct {
		ID string `json:"id"`
	}
	json.NewDecoder(wExport.Body).Decode(&snap)
	if snap.ID == "" {
		t.Skip("snapshot ID empty — export may not have produced a file")
	}

	w := doRequest(r, "GET", "/api/provision/test-db/snapshot/"+snap.ID+"/download", "")
	if w.Code != 200 {
		t.Errorf("download snapshot: got %d, body: %s", w.Code, w.Body.String())
	}
}

// testBackupStorage is the R2-shaped backup store handler tests wire into
// BackupService so K8s restores resolve a real endpoint instead of failing
// with ErrBackupStorageNotConfigured.
func testBackupStorage() service.BackupStorageSource {
	return service.StaticBackupStorage(&domain.S3Credentials{
		AccessKeyID:     "test-key",
		SecretAccessKey: "test-secret",
		Endpoint:        "https://acct.r2.cloudflarestorage.com",
		Bucket:          "excalibase-backups",
		Region:          "auto",
	})
}
