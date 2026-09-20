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

// UpdateRunningRestoreJob carries the conditional-write semantics the real
// table enforces: only the owner of a still-running job may write it.
func (f *fakeRestoreJobStoreForHandler) UpdateRunningRestoreJob(_ context.Context, j *domain.RestoreJob, owner string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	stored, ok := f.jobs[j.ID]
	if !ok || stored.Status != domain.RestoreStatusRunning || stored.Owner != owner {
		return false, nil
	}
	j.Owner = owner
	j.CreatedAt = stored.CreatedAt
	f.jobs[j.ID] = *j
	return true, nil
}

func (f *fakeRestoreJobStoreForHandler) HeartbeatRestoreJob(_ context.Context, id, owner string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	stored, ok := f.jobs[id]
	return ok && stored.Status == domain.RestoreStatusRunning && stored.Owner == owner, nil
}

func (f *fakeRestoreJobStoreForHandler) FailAbandonedRestoreJobs(context.Context, string, time.Duration, string) ([]string, error) {
	return nil, nil
}

func (f *fakeRestoreJobStoreForHandler) FindRestoreJob(_ context.Context, projectID, id string) (*domain.RestoreJob, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	j, ok := f.jobs[id]
	if !ok || (j.SourceProjectID != projectID && j.NewProjectID != projectID) {
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
	router, orch, _ := buildBackupHandlerHarness(t)
	return router, orch
}

// setupBackupHandlerWithStore exposes the harness's instance store for tests
// that assert on the rows a restore may or may not touch.
func setupBackupHandlerWithStore(t *testing.T) (*chi.Mux, *storage.FileSystemStore) {
	t.Helper()
	router, _, store := buildBackupHandlerHarness(t)
	return router, store
}

func buildBackupHandlerHarness(t *testing.T) (*chi.Mux, *service.RestoreOrchestrator, *storage.FileSystemStore) {
	t.Helper()
	dir := t.TempDir()
	store, _ := storage.NewFileSystemStore(dir)
	mock := k8s.NewMockClient()
	backupSvc := service.NewBackupService(store, mock, dir, testBackupStorage())

	store.Create(&domain.DatabaseInstance{
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
	return r, orch, store
}

func TestBackupHandler_Restore_AsyncReturnsRunningJob(t *testing.T) {
	r, _ := setupBackupHandlerWithOrchestrator(t)

	body := `{"newProjectName":"dst"}`
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
	if got.SourceProjectID != "p1" {
		t.Errorf("source id: %+v", got)
	}
	if !strings.HasPrefix(got.NewProjectID, "proj-") || got.NewProjectID == "dst" {
		t.Errorf("the restore target id must be server-generated, got %q", got.NewProjectID)
	}
	if got.NewProjectName != "dst" {
		t.Errorf("display name: %q", got.NewProjectName)
	}
	if got.Status != domain.RestoreStatusRunning {
		t.Errorf("initial status: got %s", got.Status)
	}
}

func TestBackupHandler_GetRestoreJob_ReturnsCompletedAfterRun(t *testing.T) {
	r, _ := setupBackupHandlerWithOrchestrator(t)

	body := `{"newProjectName":"dst"}`
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

// recordingBackupAdapter wraps a real K8s adapter just to record
// whether Restore was called — proves the orchestrator delegates
// instead of silently no-op'ing the actual restore.
type recordingBackupAdapter struct {
	mu          sync.Mutex
	restoreHits int
}

func (r *recordingBackupAdapter) Configure(_ context.Context, _ *domain.DatabaseInstance, _ string, _ int) error {
	return nil
}
func (r *recordingBackupAdapter) TriggerManual(_ context.Context, _ *domain.DatabaseInstance) (service.BackupRef, error) {
	return service.BackupRef{}, nil
}
func (r *recordingBackupAdapter) List(_ context.Context, _ *domain.DatabaseInstance) ([]service.BackupRef, error) {
	return nil, nil
}
func (r *recordingBackupAdapter) Restore(_ context.Context, inst *domain.DatabaseInstance, req domain.RestoreRequest) (*domain.ProvisioningResponse, error) {
	r.mu.Lock()
	r.restoreHits++
	r.mu.Unlock()
	return &domain.ProvisioningResponse{ProjectID: req.TargetProjectID, Status: "RESTORING"}, nil
}

// TestBackupHandler_Restore_OrchestratorDelegatesToAdapter pins the
// regression we shipped + reverted: orchestrator must run a step
// that calls into BackupService.RestoreFromBackup, otherwise async
// restore silently no-ops the actual work.
func TestBackupHandler_Restore_OrchestratorDelegatesToAdapter(t *testing.T) {
	dir := t.TempDir()
	store, _ := storage.NewFileSystemStore(dir)
	rec := &recordingBackupAdapter{}
	backupSvc := service.NewBackupServiceWithAdapters(store, map[domain.DeploymentMode]service.BackupAdapter{
		domain.ModeK8s: rec,
	}, dir)
	store.Create(&domain.DatabaseInstance{
		ProjectID: "p1", OrgID: "o", DeploymentMode: domain.ModeK8s, Status: "ACTIVE",
	})

	jobs := &fakeRestoreJobStoreForHandler{}
	orch := service.NewRestoreOrchestrator(service.RestoreOrchestratorConfig{Jobs: jobs})
	// Same wiring main.go uses: a single delegate step that calls
	// the legacy synchronous Restore.
	orch.SetSteps([]service.RestoreStep{
		{Name: "delegate-to-adapter", Run: func(ctx context.Context, j *domain.RestoreJob) error {
			inst, _ := store.FindByProjectID(j.SourceProjectID)
			_, err := backupSvc.RestoreFromBackup(ctx, inst.ProjectID, domain.RestoreRequest{
				NewProjectName: j.NewProjectID, TargetProjectID: j.NewProjectID,
			})
			return err
		}},
	})

	h := NewBackupHandler(backupSvc)
	h.SetRestoreOrchestrator(orch)
	r := chi.NewRouter()
	r.Route("/api/provision/{projectId}/backup", func(r chi.Router) { h.Routes(r) })

	body := `{"newProjectName":"dst"}`
	req := httptest.NewRequest("POST", "/api/provision/p1/backup/restore", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("status: %d body=%s", w.Code, w.Body.String())
	}
	var started domain.RestoreJob
	json.NewDecoder(w.Body).Decode(&started)

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		w2 := httptest.NewRecorder()
		req2 := httptest.NewRequest("GET", "/api/provision/p1/backup/restore/"+started.ID, nil)
		r.ServeHTTP(w2, req2)
		var got domain.RestoreJob
		json.NewDecoder(w2.Body).Decode(&got)
		if got.Status == domain.RestoreStatusCompleted {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	rec.mu.Lock()
	defer rec.mu.Unlock()
	if rec.restoreHits != 1 {
		t.Errorf("adapter.Restore should have been called exactly once via orchestrator step, got %d", rec.restoreHits)
	}
}

func TestBackupHandler_Restore_RejectsTwoTargets(t *testing.T) {
	r, _ := setupBackupHandlerWithOrchestrator(t)
	body := `{"newProjectName":"dst","targetXid":"123","targetLsn":"0/15"}`
	req := httptest.NewRequest("POST", "/api/provision/p1/backup/restore", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != 400 {
		t.Errorf("status: got %d, want 400 (body=%s)", w.Code, w.Body.String())
	}
}
