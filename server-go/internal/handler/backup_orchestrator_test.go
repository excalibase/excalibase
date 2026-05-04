package handler

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/k8s"
	"github.com/excalibase/provisioning-poc/internal/service"
	"github.com/excalibase/provisioning-poc/internal/storage"
)

// fakeRestoreJobStoreForHandler — minimal in-memory store for the
// orchestrator-aware Restore handler tests. Mutex-guarded because the
// orchestrator goroutine writes while the test polls.
type fakeRestoreJobStoreForHandler struct {
	mu   sync.Mutex
	jobs map[string]domain.RestoreJob
}

func (f *fakeRestoreJobStoreForHandler) UpsertRestoreJob(_ context.Context, j *domain.RestoreJob) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.jobs == nil {
		f.jobs = map[string]domain.RestoreJob{}
	}
	if existing, ok := f.jobs[j.ID]; ok {
		j.CreatedAt = existing.CreatedAt
	}
	f.jobs[j.ID] = *j
	return nil
}

func (f *fakeRestoreJobStoreForHandler) FindRestoreJob(_ context.Context, id string) (*domain.RestoreJob, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	j, ok := f.jobs[id]
	if !ok {
		return nil, nil
	}
	return &j, nil
}

func (f *fakeRestoreJobStoreForHandler) ListRunningRestoreJobs(_ context.Context) ([]domain.RestoreJob, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := []domain.RestoreJob{}
	for _, j := range f.jobs {
		if j.Status == domain.RestoreStatusRunning {
			out = append(out, j)
		}
	}
	return out, nil
}

func setupBackupHandlerWithOrchestrator(t *testing.T) (*chi.Mux, *service.RestoreOrchestrator) {
	t.Helper()
	dir := t.TempDir()
	store, _ := storage.NewFileSystemStore(dir)
	mock := k8s.NewMockClient()
	backupSvc := service.NewBackupService(store, mock, dir)

	store.Save(&domain.DatabaseInstance{
		ProjectID: "p1", OrgID: "o", DeploymentMode: domain.ModeK8s, Status: "ACTIVE",
	})

	jobs := &fakeRestoreJobStoreForHandler{}
	orch := service.NewRestoreOrchestrator(service.RestoreOrchestratorConfig{Jobs: jobs})
	orch.SetSteps([]service.RestoreStep{
		{Name: "noop", Run: func(_ context.Context, _ *domain.RestoreJob) error { return nil }},
	})

	h := NewBackupHandler(backupSvc)
	h.SetRestoreOrchestrator(orch)

	r := chi.NewRouter()
	r.Route("/api/provision/{projectId}/backup", func(r chi.Router) { h.Routes(r) })
	return r, orch
}

func TestBackupHandler_Restore_AsyncReturnsRunningJob(t *testing.T) {
	r, _ := setupBackupHandlerWithOrchestrator(t)

	body := `{"newProjectId":"dst"}`
	req := httptest.NewRequest("POST", "/api/provision/p1/backup/restore", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Errorf("status: %d body=%s", w.Code, w.Body.String())
	}
	var got domain.RestoreJob
	if err := json.NewDecoder(w.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.ID == "" {
		t.Errorf("job id missing")
	}
	if got.SourceProjectID != "p1" || got.NewProjectID != "dst" {
		t.Errorf("ids: %+v", got)
	}
	if got.Status != domain.RestoreStatusRunning {
		t.Errorf("initial status: got %s", got.Status)
	}
}

func TestBackupHandler_GetRestoreJob_ReturnsCompletedAfterRun(t *testing.T) {
	r, _ := setupBackupHandlerWithOrchestrator(t)

	body := `{"newProjectId":"dst"}`
	req := httptest.NewRequest("POST", "/api/provision/p1/backup/restore", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	var started domain.RestoreJob
	json.NewDecoder(w.Body).Decode(&started)

	// Poll the jobs route until completion.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		w2 := httptest.NewRecorder()
		req2 := httptest.NewRequest("GET", "/api/provision/p1/backup/restore/"+started.ID, nil)
		r.ServeHTTP(w2, req2)
		if w2.Code == 200 {
			var got domain.RestoreJob
			json.NewDecoder(w2.Body).Decode(&got)
			if got.Status == domain.RestoreStatusCompleted {
				return
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("job never completed")
}

func TestBackupHandler_GetRestoreJob_NotFound(t *testing.T) {
	r, _ := setupBackupHandlerWithOrchestrator(t)
	req := httptest.NewRequest("GET", "/api/provision/p1/backup/restore/missing", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != 404 {
		t.Errorf("status: got %d, want 404 (body=%s)", w.Code, w.Body.String())
	}
}

func TestBackupHandler_Restore_RejectsTwoTargets(t *testing.T) {
	r, _ := setupBackupHandlerWithOrchestrator(t)
	body := `{"newProjectId":"dst","targetXid":"123","targetLsn":"0/15"}`
	req := httptest.NewRequest("POST", "/api/provision/p1/backup/restore", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != 400 {
		t.Errorf("status: got %d, want 400 (body=%s)", w.Code, w.Body.String())
	}
}
