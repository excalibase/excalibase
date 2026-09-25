package handler

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/k8s"
	"github.com/excalibase/provisioning-poc/internal/service"
	"github.com/excalibase/provisioning-poc/internal/storage"
)

// setupBackupHandlerForDocumentDB mounts the backup routes over a project
// that carries the DocumentDB flag, so EXC-409's refusal can be exercised
// at the HTTP boundary — before the orchestrator files a job or a project
// id is even allocated.
func setupBackupHandlerForDocumentDB(t *testing.T, documentDB bool) (*chi.Mux, *fakeRestoreJobStoreForHandler, *storage.FileSystemStore) {
	t.Helper()
	dir := t.TempDir()
	store, _ := storage.NewFileSystemStore(dir)
	mock := k8s.NewMockClient()
	backupSvc := service.NewBackupService(store, mock, dir, testBackupStorage())
	backupSvc.SetOrgProjectCapacity(unlimitedCapacity{})
	store.Create(&domain.DatabaseInstance{
		ProjectID: "p1", OrgID: "o", DeploymentMode: domain.ModeK8s, Status: "ACTIVE",
		DocumentDB: documentDB,
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
	return r, jobs, store
}

// TestBackupHandler_Restore_RefusesDocumentDBProject: EXC-409, owner
// decision — restoring a DocumentDB project is refused before anything is
// created: no restore job is filed and no project id is consumed.
func TestBackupHandler_Restore_RefusesDocumentDBProject(t *testing.T) {
	r, jobs, store := setupBackupHandlerForDocumentDB(t, true)

	body := `{"newProjectName":"copy"}`
	req := httptest.NewRequest("POST", "/api/provision/p1/backup/restore", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != 409 {
		t.Fatalf("status: got %d, want 409 (body=%s)", w.Code, w.Body.String())
	}
	var body2 map[string]interface{}
	if err := json.NewDecoder(w.Body).Decode(&body2); err == nil {
		if msg, _ := body2["error"].(string); !strings.Contains(msg, "DocumentDB") {
			t.Errorf("error message must explain the refusal, got %q", msg)
		}
	}

	jobs.mu.Lock()
	jobCount := len(jobs.jobs)
	jobs.mu.Unlock()
	if jobCount != 0 {
		t.Errorf("no restore job must be filed for a refused DocumentDB restore, got %d", jobCount)
	}
	if got, _ := store.FindByProjectID("copy"); got != nil {
		t.Error("no project row must exist for a refused DocumentDB restore")
	}
}

// TestBackupHandler_Restore_StillProceedsForPlainPostgres pins that the
// DocumentDB refusal in the handler does not catch an ordinary project.
func TestBackupHandler_Restore_StillProceedsForPlainPostgres(t *testing.T) {
	r, _, _ := setupBackupHandlerForDocumentDB(t, false)

	body := `{"newProjectName":"copy"}`
	req := httptest.NewRequest("POST", "/api/provision/p1/backup/restore", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("status: got %d, want 200 (body=%s)", w.Code, w.Body.String())
	}
}
