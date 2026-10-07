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

// fullPlan is an organisation whose one project slot is taken.
type fullPlan struct{}

func (fullPlan) EnsureOrgCanTakeProject(context.Context, string) error {
	return &service.OrgProjectLimitError{Limit: 1, Tier: domain.Free}
}

func (fullPlan) RequireRestoreDiskFits(context.Context, *domain.DatabaseInstance) error { return nil }

type restoreModesHarness struct {
	router *chi.Mux
	store  *storage.FileSystemStore
	jobs   *fakeRestoreJobStoreForHandler
	ran    chan domain.RestoreJob
	hold   chan struct{}
}

func newRestoreModesHarness(t *testing.T, project *domain.DatabaseInstance) *restoreModesHarness {
	t.Helper()
	dir := t.TempDir()
	store, _ := storage.NewFileSystemStore(dir)
	backupSvc := service.NewBackupService(store, k8s.NewMockClient(), dir, testBackupStorage())
	backupSvc.SetOrgProjectCapacity(fullPlan{})
	if err := store.Create(project); err != nil {
		t.Fatal(err)
	}
	h := &restoreModesHarness{store: store, jobs: &fakeRestoreJobStoreForHandler{}, ran: make(chan domain.RestoreJob, 4), hold: make(chan struct{})}
	t.Cleanup(func() { close(h.hold) })
	orch := service.NewRestoreOrchestrator(service.RestoreOrchestratorConfig{Jobs: h.jobs})
	orch.SetSteps([]service.RestoreStep{{Name: domain.RestoreStepRestoring, Run: func(_ context.Context, j *domain.RestoreJob) error {
		h.ran <- *j
		<-h.hold
		return nil
	}}})
	bh := NewBackupHandler(backupSvc)
	bh.SetRestoreOrchestrator(orch)
	h.router = chi.NewRouter()
	h.router.Route("/api/provision/{projectId}/backup", func(r chi.Router) { bh.Routes(r) })
	return h
}

func (h *restoreModesHarness) post(body string) (*httptest.ResponseRecorder, map[string]any) {
	req := httptest.NewRequest("POST", "/api/provision/p1/backup/restore", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.router.ServeHTTP(w, req)
	out := map[string]any{}
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	return w, out
}

func backedUpProject() *domain.DatabaseInstance {
	on := true
	return &domain.DatabaseInstance{ProjectID: "p1", OrgID: "o", DeploymentMode: domain.ModeK8s, Status: "ACTIVE", BackupEnabled: &on}
}

// On a plan with no free slot a copy is refused with a code Studio can
// offer the in-place restore on, and a sentence saying so.
func TestRestoreIntoANewProjectAtThePlanLimitOffersTheReplace(t *testing.T) {
	h := newRestoreModesHarness(t, backedUpProject())

	w, body := h.post(`{"newProjectName":"copy"}`)
	if w.Code != 409 {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	if body["code"] != "project_limit_reached" {
		t.Errorf("code: %v", body["code"])
	}
	if msg, _ := body["error"].(string); !strings.Contains(msg, "project limit of 1") || !strings.Contains(msg, "restore into this project") {
		t.Errorf("message: %q", msg)
	}
}

func TestRestoreInPlaceNeedsNoSlotAndTargetsTheProject(t *testing.T) {
	h := newRestoreModesHarness(t, backedUpProject())

	w, body := h.post(`{"mode":"in_place","confirmReplace":true,"targetTime":"2026-10-07T12:00:00Z"}`)
	if w.Code != 200 {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	if body["mode"] != domain.RestoreModeInPlace || body["newProjectId"] != "p1" || body["status"] != domain.RestoreStatusRunning {
		t.Errorf("job: %v", body)
	}
	job := <-h.ran
	if !job.Request.InPlace() || job.Request.TargetProjectID != "p1" {
		t.Errorf("the step must be handed the in-place request: %+v", job.Request)
	}
}

func TestRestoreInPlaceMustBeConfirmed(t *testing.T) {
	h := newRestoreModesHarness(t, backedUpProject())
	w, body := h.post(`{"mode":"in_place"}`)
	if w.Code != 400 || !strings.Contains(body["error"].(string), "confirm") {
		t.Fatalf("status %d: %v", w.Code, body)
	}
}

// A second click while the first restore runs starts nothing.
func TestRestoreTwiceWhileTheFirstRunsIsRefused(t *testing.T) {
	h := newRestoreModesHarness(t, backedUpProject())
	first := `{"mode":"in_place","confirmReplace":true,"targetTime":"2026-10-07T12:00:00Z"}`
	if w, _ := h.post(first); w.Code != 200 {
		t.Fatalf("first: %d %s", w.Code, w.Body.String())
	}
	<-h.ran
	w, body := h.post(first)
	if w.Code != 409 || !strings.Contains(body["error"].(string), "already running") {
		t.Fatalf("second: %d %v", w.Code, body)
	}
	if len(h.jobs.jobs) != 1 {
		t.Errorf("jobs: %d", len(h.jobs.jobs))
	}
}

func TestRestoreInPlaceOfAProjectWithoutBackupsIsRefused(t *testing.T) {
	project := backedUpProject()
	project.BackupEnabled = nil
	h := newRestoreModesHarness(t, project)
	w, body := h.post(`{"mode":"in_place","confirmReplace":true}`)
	if w.Code != 409 || body["error"] != service.ErrInPlaceNoBackups.Error() {
		t.Fatalf("%d %v", w.Code, body)
	}
}

func TestRestoreInPlaceOfAPausedProjectIsRefused(t *testing.T) {
	project := backedUpProject()
	project.Status = string(domain.StatusPaused)
	h := newRestoreModesHarness(t, project)
	w, _ := h.post(`{"mode":"in_place","confirmReplace":true}`)
	if w.Code != 409 {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
}

func TestRestoreInPlaceOnDockerIsRefused(t *testing.T) {
	project := backedUpProject()
	project.DeploymentMode = domain.ModeDocker
	h := newRestoreModesHarness(t, project)
	w, body := h.post(`{"mode":"in_place","confirmReplace":true}`)
	if w.Code != 409 || body["error"] != service.ErrInPlaceRestoreUnsupported.Error() {
		t.Fatalf("%d %v", w.Code, body)
	}
}
