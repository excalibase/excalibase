package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/k8s"
	"github.com/excalibase/provisioning-poc/internal/service"
	"github.com/excalibase/provisioning-poc/internal/storage"
)

// seed puts a job straight into the fake store, without running the pipeline.
func (f *fakeRestoreJobStoreForHandler) seed(job domain.RestoreJob) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.jobs == nil {
		f.jobs = map[string]domain.RestoreJob{}
	}
	f.jobs[job.ID] = job
}

// setupBackupHandlerWithJobs mounts the backup routes and hands back the job
// store so a test can seed rows belonging to other projects.
func setupBackupHandlerWithJobs(t *testing.T) (chi.Router, *fakeRestoreJobStoreForHandler) {
	t.Helper()
	dir := t.TempDir()
	store, _ := storage.NewFileSystemStore(dir)
	mock := k8s.NewMockClient()
	backupSvc := service.NewBackupService(store, mock, dir, testBackupStorage())
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

	router := chi.NewRouter()
	router.Route("/api/provision/{projectId}/backup", func(r chi.Router) { h.Routes(r) })
	return router, jobs
}

func getRestoreJob(t *testing.T, router http.Handler, projectID, jobID string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("GET", "/api/provision/"+projectID+"/backup/restore/"+jobID, nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	return w
}

// TestGetRestoreJob_RejectsOtherProject reproduces EXC-399: an admin of
// project A must not read project B's restore job by naming B's job id under
// A's route.
func TestGetRestoreJob_RejectsOtherProject(t *testing.T) {
	router, jobs := setupBackupHandlerWithJobs(t)
	jobs.seed(domain.RestoreJob{
		ID: "job-b", SourceProjectID: "p-b", NewProjectID: "p-b-restored",
		Status: domain.RestoreStatusFailed, FailureReason: "pg_basebackup exited 1",
	})

	w := getRestoreJob(t, router, "p1", "job-b")
	if w.Code != http.StatusNotFound {
		t.Errorf("cross-project restore job: got %d, want 404 (body: %s)", w.Code, w.Body.String())
	}
}

// TestGetRestoreJob_SourceAndTargetProjectsBothSee pins the legitimate
// readers: the row names two projects, and each route is already bound to a
// caller with access to the project it names.
func TestGetRestoreJob_SourceAndTargetProjectsBothSee(t *testing.T) {
	router, jobs := setupBackupHandlerWithJobs(t)
	jobs.seed(domain.RestoreJob{
		ID: "job-1", SourceProjectID: "p1", NewProjectID: "p1-restored",
		Status: domain.RestoreStatusCompleted,
	})

	for _, projectID := range []string{"p1", "p1-restored"} {
		w := getRestoreJob(t, router, projectID, "job-1")
		if w.Code != http.StatusOK {
			t.Fatalf("%s polling its own job: got %d (body: %s)", projectID, w.Code, w.Body.String())
		}
		var got domain.RestoreJob
		if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if got.ID != "job-1" {
			t.Errorf("job id: got %q, want job-1", got.ID)
		}
	}
}

// TestGetRestoreJob_UnknownID stays a 404.
func TestGetRestoreJob_UnknownID(t *testing.T) {
	router, _ := setupBackupHandlerWithJobs(t)
	if w := getRestoreJob(t, router, "p1", "nope"); w.Code != http.StatusNotFound {
		t.Errorf("unknown job: got %d, want 404", w.Code)
	}
}
