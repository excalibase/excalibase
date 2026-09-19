package handler

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/k8s"
	"github.com/excalibase/provisioning-poc/internal/service"
	"github.com/excalibase/provisioning-poc/internal/testutil/fakestore"
	"github.com/go-chi/chi/v5"
)

// A restore body naming another org's project must not decide where the
// restore lands: the id is generated, the victim keeps its row (EXC-415).
func TestBackupHandler_Restore_IgnoresACallerSuppliedProjectID(t *testing.T) {
	r, store := setupBackupHandlerWithStore(t)
	victim := &domain.DatabaseInstance{
		ProjectID: "proj-victim01", OrgID: "org-victim", Namespace: "org-victim-proj-victim01",
		Host: "victim-rw", Username: "victim_admin", Password: "victim-password",
		DeploymentMode: domain.ModeK8s, Status: "ACTIVE",
	}
	if err := store.Create(victim); err != nil {
		t.Fatalf("seed victim: %v", err)
	}

	body := `{"newProjectName":"copy","newProjectId":"proj-victim01"}`
	req := httptest.NewRequest("POST", "/api/provision/p1/backup/restore", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("status: %d body=%s", w.Code, w.Body.String())
	}
	var job domain.RestoreJob
	if err := json.NewDecoder(w.Body).Decode(&job); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if job.NewProjectID == victim.ProjectID {
		t.Fatal("the restore target must not be the project id from the request body")
	}

	got, _ := store.FindByProjectID(victim.ProjectID)
	if got.OrgID != victim.OrgID || got.Host != victim.Host || got.Password != victim.Password {
		t.Errorf("victim row was rewritten: org=%q host=%q", got.OrgID, got.Host)
	}
}

// A restore without a display name is rejected — it is the only naming the
// caller supplies.
func TestBackupHandler_Restore_RequiresADisplayName(t *testing.T) {
	r, _ := setupBackupHandlerWithStore(t)

	req := httptest.NewRequest("POST", "/api/provision/p1/backup/restore", strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != 400 {
		t.Errorf("status: got %d, want 400 (body=%s)", w.Code, w.Body.String())
	}
}

// EXC-399's project-scoped polling has to keep working now that the target id
// is generated (EXC-415): the client learns the id from the restore job, the
// new project can poll under it, and a project named nowhere on the job cannot.
func TestBackupHandler_GeneratedRestoreTargetCanPollItsOwnJob(t *testing.T) {
	router, _ := setupBackupHandlerWithJobs(t)

	req := httptest.NewRequest("POST", "/api/provision/p1/backup/restore",
		strings.NewReader(`{"newProjectName":"copy"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("restore: %d body=%s", w.Code, w.Body.String())
	}
	var job domain.RestoreJob
	if err := json.NewDecoder(w.Body).Decode(&job); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if job.NewProjectID == "" {
		t.Fatal("the restore job must report the generated target project id")
	}

	if got := getRestoreJob(t, router, job.NewProjectID, job.ID); got.Code != 200 {
		t.Errorf("the restored project must reach its own job: got %d", got.Code)
	}
	if got := getRestoreJob(t, router, "proj-stranger1", job.ID); got.Code != 404 {
		t.Errorf("an unrelated project must not reach the job: got %d", got.Code)
	}
}

// When no project id can be allocated the restore is refused outright: the
// handler never falls back to a caller-supplied or invented id.
func TestBackupHandler_Restore_FailsWhenNoProjectIDCanBeAllocated(t *testing.T) {
	instances := fakestore.NewInstances()
	if err := instances.Create(&domain.DatabaseInstance{
		ProjectID: "p1", OrgID: "o", DeploymentMode: domain.ModeK8s, Status: "ACTIVE",
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	instances.Err = errors.New("platform db down")

	dir := t.TempDir()
	h := NewBackupHandler(service.NewBackupService(instances, k8s.NewMockClient(), dir, testBackupStorage()))
	router := chi.NewRouter()
	router.Route("/api/provision/{projectId}/backup", func(r chi.Router) { h.Routes(r) })

	req := httptest.NewRequest("POST", "/api/provision/p1/backup/restore",
		strings.NewReader(`{"newProjectName":"copy"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("status: got %d, want 503 (body: %s)", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "platform db down") {
		t.Error("the response must not leak the store failure")
	}
}
