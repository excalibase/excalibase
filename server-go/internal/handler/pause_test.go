package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/k8s"
	"github.com/excalibase/provisioning-poc/internal/provisioner"
	"github.com/excalibase/provisioning-poc/internal/service"
	"github.com/excalibase/provisioning-poc/internal/storage"
)

// fakePauserForHandler — minimal Pauser satisfying the interface.
type fakePauserForHandler struct {
	mu          sync.Mutex
	paused      bool
	resumed     bool
	pauseErr    error
	resumeErr   error
}

func (f *fakePauserForHandler) StopReplication(_ context.Context, _, _ string) error { return nil }

func (f *fakePauserForHandler) Pause(_ context.Context, _, _ string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.paused = true
	return f.pauseErr
}
func (f *fakePauserForHandler) Resume(_ context.Context, _, _ string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.resumed = true
	return f.resumeErr
}

// fakeBackupTriggerForHandler — counts trigger calls.
type fakeBackupTriggerForHandler struct {
	calls int
	// status overrides what the pre-pause backup is observed to report.
	status string
}

func (f *fakeBackupTriggerForHandler) BackupsConfigured(string) (bool, error) { return true, nil }

func (f *fakeBackupTriggerForHandler) BackupStatus(_ context.Context, _, _ string) (string, error) {
	if f.status != "" {
		return f.status, nil
	}
	return "COMPLETED", nil
}

func (f *fakeBackupTriggerForHandler) TriggerManualBackup(_ context.Context, _ string) (map[string]interface{}, error) {
	f.calls++
	return map[string]interface{}{"id": "bk-1", "status": "IN_PROGRESS"}, nil
}

func setupPauseHandler(t *testing.T) (*chi.Mux, *storage.FileSystemStore, *fakePauserForHandler, *fakeBackupTriggerForHandler) {
	t.Helper()
	dir := t.TempDir()
	store, _ := storage.NewFileSystemStore(dir)
	mock := k8s.NewMockClient()
	provSvc := service.NewProvisioningService(store, provisioner.NewFactory(), mock)

	pauser := &fakePauserForHandler{}
	bk := &fakeBackupTriggerForHandler{}
	pauseSvc := service.NewPauseService(service.PauseServiceConfig{
		Instances: store,
		Pausers:   map[domain.DeploymentMode]provisioner.Pauser{domain.ModeDocker: pauser, domain.ModeK8s: pauser},
		Backups:   bk,
	})

	h := NewProvisioningHandler(provSvc, nil)
	h.SetPauseService(pauseSvc)
	h.SetInstanceStore(store)

	r := chi.NewRouter()
	r.Route("/api/provision", func(r chi.Router) { h.Routes(r) })
	return r, store, pauser, bk
}

// seedPausableProject creates the project the observed-pause handler tests
// act on.
func seedPausableProject(t *testing.T, store *storage.FileSystemStore) {
	t.Helper()
	if err := store.Create(&domain.DatabaseInstance{
		ProjectID: "pause-db", OrgID: "o", Status: "ACTIVE",
		DeploymentMode: domain.ModeDocker, Tier: domain.Free, Namespace: "container-pause-db",
	}); err != nil {
		t.Fatalf("seed project: %v", err)
	}
}

func TestPauseHandler_Pause_HappyPath(t *testing.T) {
	r, store, pauser, bk := setupPauseHandler(t)
	store.Create(&domain.DatabaseInstance{
		ProjectID: "p1", OrgID: "o", Status: "ACTIVE",
		DeploymentMode: domain.ModeDocker, Tier: domain.Free, Namespace: "container-p1",
	})

	req := httptest.NewRequest("POST", "/api/provision/p1/pause",
		strings.NewReader(`{"reason":"manual"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("status: %d body=%s", w.Code, w.Body.String())
	}
	if bk.calls != 1 {
		t.Errorf("backup must run before pause: got %d", bk.calls)
	}
	if !pauser.paused {
		t.Errorf("provisioner Pause never called")
	}
	got, _ := store.FindByProjectID("p1")
	if got.Status != string(domain.StatusPaused) {
		t.Errorf("status: got %q, want PAUSED", got.Status)
	}
	var resp map[string]interface{}
	json.NewDecoder(w.Body).Decode(&resp)
	if resp["status"] != "PAUSED" {
		t.Errorf("response status: %v", resp["status"])
	}
}

func TestPauseHandler_Pause_Idempotent(t *testing.T) {
	r, store, pauser, bk := setupPauseHandler(t)
	store.Create(&domain.DatabaseInstance{
		ProjectID: "p1", OrgID: "o", Status: string(domain.StatusPaused),
		DeploymentMode: domain.ModeDocker, Tier: domain.Free, Namespace: "container-p1",
		PauseReason: domain.PauseReasonIdle7Days,
	})

	req := httptest.NewRequest("POST", "/api/provision/p1/pause", strings.NewReader(`{}`))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("status: %d", w.Code)
	}
	if bk.calls != 0 || pauser.paused {
		t.Errorf("must not re-pause already-paused project")
	}
}

func TestPauseHandler_Pause_ModeWithoutPauserRefused(t *testing.T) {
	r, store, _, _ := setupPauseHandler(t)
	store.Create(&domain.DatabaseInstance{
		ProjectID: "unwired-1", OrgID: "o", Status: "ACTIVE",
		DeploymentMode: domain.DeploymentMode("unwired"),
	})

	req := httptest.NewRequest("POST", "/api/provision/unwired-1/pause", strings.NewReader(`{}`))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != 400 {
		t.Errorf("pause on a mode with no pauser should 400, got %d (body=%s)", w.Code, w.Body.String())
	}
}

func TestPauseHandler_Resume_HappyPath(t *testing.T) {
	r, store, pauser, _ := setupPauseHandler(t)
	store.Create(&domain.DatabaseInstance{
		ProjectID: "p1", OrgID: "o", Status: string(domain.StatusPaused),
		DeploymentMode: domain.ModeDocker, Tier: domain.Free, Namespace: "container-p1",
		PauseReason: domain.PauseReasonIdle7Days,
	})

	req := httptest.NewRequest("POST", "/api/provision/p1/resume", strings.NewReader(`{}`))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("status: %d body=%s", w.Code, w.Body.String())
	}
	if !pauser.resumed {
		t.Errorf("provisioner Resume never called")
	}
	got, _ := store.FindByProjectID("p1")
	if got.Status != "ACTIVE" {
		t.Errorf("status after resume: %q, want ACTIVE", got.Status)
	}
	if got.PauseReason != "" {
		t.Errorf("PauseReason should clear: %q", got.PauseReason)
	}
}

func TestPauseHandler_NotFound(t *testing.T) {
	r, _, _, _ := setupPauseHandler(t)
	req := httptest.NewRequest("POST", "/api/provision/missing/pause", strings.NewReader(`{}`))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != 404 {
		t.Errorf("missing project should 404, got %d", w.Code)
	}
}

func TestPauseHandler_NoServiceConfigured(t *testing.T) {
	dir := t.TempDir()
	store, _ := storage.NewFileSystemStore(dir)
	mock := k8s.NewMockClient()
	provSvc := service.NewProvisioningService(store, provisioner.NewFactory(), mock)
	h := NewProvisioningHandler(provSvc, nil)
	// Don't call SetPauseService — pause endpoint should return 503
	r := chi.NewRouter()
	r.Route("/api/provision", func(r chi.Router) { h.Routes(r) })
	store.Create(&domain.DatabaseInstance{ProjectID: "p1", OrgID: "o", Status: "ACTIVE"})

	req := httptest.NewRequest("POST", "/api/provision/p1/pause", strings.NewReader(`{}`))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != 503 {
		t.Errorf("no service: should 503, got %d", w.Code)
	}
	_ = errors.New // silence unused import for now
}
