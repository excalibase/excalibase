package handler

import (
	"context"
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
	return setupBackupHandlerForDocumentDBOn(t, mode, documentDB, true)
}

// setupBackupHandlerForDocumentDBOn is the same on an installation that does
// or does not run DocumentDB.
func setupBackupHandlerForDocumentDBOn(t *testing.T, mode domain.DeploymentMode, documentDB, documentDBInstalled bool) (*chi.Mux, *fakeRestoreJobStoreForHandler, *storage.FileSystemStore) {
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
	h.SetDocumentDBEnabled(documentDBInstalled)

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

// TestBackupHandler_Restore_AcceptsDockerDocumentDBProject: EXC-576 — a
// single host restores a DocumentDB project with its gateway.
func TestBackupHandler_Restore_AcceptsDockerDocumentDBProject(t *testing.T) {
	r, jobs, _ := setupBackupHandlerForDocumentDB(t, domain.ModeDocker, true)

	w := postRestore(r)

	if w.Code != 200 {
		t.Fatalf("status: got %d, want 200 (body=%s)", w.Code, w.Body.String())
	}
	if filedJobs(jobs) != 1 {
		t.Errorf("one restore job must be filed, got %d", filedJobs(jobs))
	}
}

// TestBackupHandler_Restore_RefusesDockerDocumentDBWhereItIsNotInstalled: a
// single host with DocumentDB off refuses it like Kubernetes does.
func TestBackupHandler_Restore_RefusesDockerDocumentDBWhereItIsNotInstalled(t *testing.T) {
	r, jobs, _ := setupBackupHandlerForDocumentDBOn(t, domain.ModeDocker, true, false)
	w := postRestore(r)
	if w.Code != 409 || !strings.Contains(w.Body.String(), "DocumentDB is not installed") || filedJobs(jobs) != 0 {
		t.Fatalf("got %d %s jobs %d", w.Code, w.Body.String(), filedJobs(jobs))
	}
}

// TestBackupHandler_Restore_StillProceedsForPlainPostgres pins that an
// ordinary docker project is restored.
func TestBackupHandler_Restore_StillProceedsForPlainPostgres(t *testing.T) {
	r, _, _ := setupBackupHandlerForDocumentDB(t, domain.ModeDocker, false)

	w := postRestore(r)

	if w.Code != 200 {
		t.Fatalf("status: got %d, want 200 (body=%s)", w.Code, w.Body.String())
	}
}

// EXC-394: a DocumentDB project cannot be restored on an installation that
// no longer runs DocumentDB; refused before a job is filed.
func TestBackupHandler_Restore_RefusesDocumentDBWhereItIsNotInstalled(t *testing.T) {
	r, jobs, _ := setupBackupHandlerForDocumentDBOn(t, domain.ModeK8s, true, false)
	w := postRestore(r)
	if w.Code != 409 || !strings.Contains(w.Body.String(), "DocumentDB is not installed") {
		t.Fatalf("got %d %s, want 409 naming the missing DocumentDB", w.Code, w.Body.String())
	}
	if filedJobs(jobs) != 0 {
		t.Fatal("a refused restore filed a job")
	}
}
