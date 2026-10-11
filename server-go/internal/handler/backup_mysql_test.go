package handler

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/k8s"
	"github.com/excalibase/provisioning-poc/internal/service"
	"github.com/excalibase/provisioning-poc/internal/storage"
)

// windowedAdapter stands in for the MOCO adapter: a fixed restore window.
type windowedAdapter struct {
	window service.RestoreWindow
}

func (windowedAdapter) Configure(context.Context, *domain.DatabaseInstance, string, int) error {
	return nil
}
func (windowedAdapter) BackupsConfigured() bool { return true }
func (windowedAdapter) TriggerManual(context.Context, *domain.DatabaseInstance) (service.BackupRef, error) {
	return service.BackupRef{}, nil
}
func (windowedAdapter) List(context.Context, *domain.DatabaseInstance) ([]service.BackupRef, error) {
	return []service.BackupRef{{ID: "20261011-060000", Status: "COMPLETED"}}, nil
}
func (windowedAdapter) Restore(context.Context, *domain.DatabaseInstance, domain.RestoreRequest) (*domain.ProvisioningResponse, error) {
	return &domain.ProvisioningResponse{}, nil
}
func (a windowedAdapter) RestoreWindow(context.Context, *domain.DatabaseInstance) (*service.RestoreWindow, error) {
	return &a.window, nil
}
func (a windowedAdapter) CheckRestoreTarget(_ context.Context, _ *domain.DatabaseInstance, req domain.RestoreRequest) error {
	if req.TargetXID != "" || (req.TargetTime != nil && req.TargetTime.After(a.window.Latest)) {
		return &service.RestoreTargetError{Message: "outside the " + a.window.String()}
	}
	return nil
}

func mysqlBackupRouter(t *testing.T) *chi.Mux {
	t.Helper()
	dir := t.TempDir()
	store, _ := storage.NewFileSystemStore(dir)
	backupSvc := service.NewBackupService(store, k8s.NewMockClient(), dir, testBackupStorage())
	backupSvc.SetOrgProjectCapacity(unlimitedCapacity{})
	backupSvc.RegisterEngineAdapter(domain.ModeK8s, domain.MySQL, windowedAdapter{window: service.RestoreWindow{
		Earliest: time.Date(2026, 10, 11, 0, 0, 0, 0, time.UTC), Latest: time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC),
	}})
	enabled := true
	if err := store.Create(&domain.DatabaseInstance{
		ProjectID: "my1", OrgID: "o", DBType: domain.MySQL, DeploymentMode: domain.ModeK8s, Status: "ACTIVE", BackupEnabled: &enabled,
	}); err != nil {
		t.Fatal(err)
	}
	jobs := &fakeRestoreJobStoreForHandler{}
	orch := service.NewRestoreOrchestrator(service.RestoreOrchestratorConfig{Jobs: jobs})
	orch.SetSteps([]service.RestoreStep{{Name: "noop", Run: func(context.Context, *domain.RestoreJob) error { return nil }}})
	h := NewBackupHandler(backupSvc)
	h.SetRestoreOrchestrator(orch)
	router := chi.NewRouter()
	router.Route("/api/provision/{projectId}/backup", func(r chi.Router) { h.Routes(r) })
	return router
}

func postMySQLRestore(router *chi.Mux, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest("POST", "/api/provision/my1/backup/restore", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	return w
}

// Owner decision: a MySQL restore is to a time inside the backup window;
// anything else is refused at submission, naming the window.
func TestMySQLRestoreTargetsAreCheckedAtSubmission(t *testing.T) {
	router := mysqlBackupRouter(t)
	for name, body := range map[string]string{
		"a transaction id": `{"newProjectName":"copy","targetXid":"42"}`,
		"after the window": `{"newProjectName":"copy","targetTime":"2026-10-11T13:00:00Z"}`,
		"in place, after":  `{"mode":"in_place","confirmReplace":true,"targetTime":"2026-10-11T13:00:00Z"}`,
	} {
		w := postMySQLRestore(router, body)
		if w.Code != 400 || !strings.Contains(w.Body.String(), "point-in-time within the backup window: 2026-10-11T00:00:00Z – 2026-10-11T12:00:00Z") {
			t.Errorf("%s: %d %s", name, w.Code, w.Body.String())
		}
	}
	if w := postMySQLRestore(router, `{"newProjectName":"copy","targetTime":"2026-10-11T09:00:00Z"}`); w.Code != 200 {
		t.Errorf("a time inside the window: %d %s", w.Code, w.Body.String())
	}
}

func TestMySQLBackupListCarriesTheRestoreWindow(t *testing.T) {
	router := mysqlBackupRouter(t)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest("GET", "/api/provision/my1/backup/list", nil))
	if w.Code != 200 {
		t.Fatalf("list %d %s", w.Code, w.Body.String())
	}
	var body struct {
		RestoreWindow *struct{ Earliest, Latest time.Time } `json:"restoreWindow"`
	}
	if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.RestoreWindow == nil || !body.RestoreWindow.Latest.Equal(time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)) {
		t.Errorf("restoreWindow %+v", body.RestoreWindow)
	}
}

// Postgres's list has no window: its point-in-time reach is continuous.
func TestPostgresBackupListHasNoRestoreWindow(t *testing.T) {
	router, _ := setupBackupHandlerWithStore(t)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest("GET", "/api/provision/p1/backup/list", nil))
	if strings.Contains(w.Body.String(), "restoreWindow") {
		t.Errorf("a Postgres list carries a window: %s", w.Body.String())
	}
}
