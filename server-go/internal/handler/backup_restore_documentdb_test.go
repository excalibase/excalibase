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
// in the given mode that may carry the DocumentDB flag.
func setupBackupHandlerForDocumentDB(t *testing.T, mode domain.DeploymentMode, documentDB bool) (*chi.Mux, *fakeRestoreJobStoreForHandler, *storage.FileSystemStore) {
	t.Helper()
	dir := t.TempDir()
	store, _ := storage.NewFileSystemStore(dir)
	mock := k8s.NewMockClient()
	backupSvc := service.NewBackupService(store, mock, dir, testBackupStorage())
	backupSvc.SetBackupCredentials(testBackupCredentials(t))
	backupSvc.SetOrgProjectCapacity(unlimitedCapacity{})
	store.Create(&domain.DatabaseInstance{
		ProjectID: "p1", OrgID: "o", DeploymentMode: mode, Status: "ACTIVE",
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

func postRestore(r *chi.Mux) *httptest.ResponseRecorder {
	req := httptest.NewRequest("POST", "/api/provision/p1/backup/restore", strings.NewReader(`{"newProjectName":"copy"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func filedJobs(jobs *fakeRestoreJobStoreForHandler) int {
	jobs.mu.Lock()
	defer jobs.mu.Unlock()
	return len(jobs.jobs)
}

// TestBackupHandler_Restore_AcceptsKubernetesDocumentDBProject: EXC-522 —
// a DocumentDB project on Kubernetes is restored like any other.
func TestBackupHandler_Restore_AcceptsKubernetesDocumentDBProject(t *testing.T) {
	r, jobs, _ := setupBackupHandlerForDocumentDB(t, domain.ModeK8s, true)

	w := postRestore(r)

	if w.Code != 200 {
		t.Fatalf("status: got %d, want 200 (body=%s)", w.Code, w.Body.String())
	}
	if filedJobs(jobs) != 1 {
		t.Errorf("one restore job must be filed, got %d", filedJobs(jobs))
	}
}

// TestBackupHandler_Restore_RefusesDockerDocumentDBProject: docker mode has
// no DocumentDB restore, so it is refused at submission, with the reason,
// before a job is filed or a project id consumed.
func TestBackupHandler_Restore_RefusesDockerDocumentDBProject(t *testing.T) {
	r, jobs, store := setupBackupHandlerForDocumentDB(t, domain.ModeDocker, true)

	w := postRestore(r)

	if w.Code != 409 {
		t.Fatalf("status: got %d, want 409 (body=%s)", w.Code, w.Body.String())
	}
	var body map[string]interface{}
	if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if msg, _ := body["error"].(string); !strings.Contains(msg, "DocumentDB") || !strings.Contains(msg, "Kubernetes") {
		t.Errorf("error message must explain the refusal, got %q", msg)
	}
	if filedJobs(jobs) != 0 {
		t.Errorf("no restore job must be filed, got %d", filedJobs(jobs))
	}
	if got, _ := store.FindByProjectID("copy"); got != nil {
		t.Error("no project row must exist for a refused restore")
	}
}

// TestBackupHandler_Restore_StillProceedsForPlainPostgres pins that the
// docker DocumentDB refusal does not catch an ordinary project.
func TestBackupHandler_Restore_StillProceedsForPlainPostgres(t *testing.T) {
	r, _, _ := setupBackupHandlerForDocumentDB(t, domain.ModeDocker, false)

	w := postRestore(r)

	if w.Code != 200 {
		t.Fatalf("status: got %d, want 200 (body=%s)", w.Code, w.Body.String())
	}
}
