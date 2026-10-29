package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/k8s"
	"github.com/excalibase/provisioning-poc/internal/service"
	"github.com/excalibase/provisioning-poc/internal/storage"
)

// EXC-667: POST /backup/copy files a restore into a new project at now, the
// latest backup or a time, and audits it.

// roomyPlan is an organisation with a free slot and room on disk.
type roomyPlan struct{}

func (roomyPlan) EnsureOrgCanTakeProject(context.Context, string) error { return nil }
func (roomyPlan) RequireRestoreDiskFits(context.Context, *domain.DatabaseInstance) error {
	return nil
}

type copyHarness struct {
	router *chi.Mux
	jobs   *fakeRestoreJobStoreForHandler
	audit  *recordedAudit
	ran    chan domain.RestoreJob
	hold   chan struct{}
}

func newCopyHarness(t *testing.T, project *domain.DatabaseInstance, plan service.OrgProjectCapacity, withOrchestrator bool) *copyHarness {
	t.Helper()
	dir := t.TempDir()
	store, _ := storage.NewFileSystemStore(dir)
	backupSvc := service.NewBackupService(store, k8s.NewMockClient(), dir, testBackupStorage())
	backupSvc.SetOrgProjectCapacity(plan)
	if err := store.Create(project); err != nil {
		t.Fatal(err)
	}
	h := &copyHarness{jobs: &fakeRestoreJobStoreForHandler{}, audit: &recordedAudit{}, ran: make(chan domain.RestoreJob, 4), hold: make(chan struct{})}
	t.Cleanup(func() { close(h.hold) })
	bh := NewBackupHandler(backupSvc)
	bh.SetAudit(h.audit)
	if withOrchestrator {
		orch := service.NewRestoreOrchestrator(service.RestoreOrchestratorConfig{Jobs: h.jobs, Slots: unmeteredSlots{}})
		orch.SetSteps([]service.RestoreStep{{Name: domain.RestoreStepRestoring, Run: func(_ context.Context, j *domain.RestoreJob) error {
			h.ran <- *j
			<-h.hold
			return nil
		}}})
		bh.SetRestoreOrchestrator(orch)
	}
	h.router = chi.NewRouter()
	h.router.Route("/api/provision/{projectId}/backup", func(r chi.Router) {
		r.Post("/copy", bh.Copy)
	})
	return h
}

func (h *copyHarness) post(body string) (*httptest.ResponseRecorder, map[string]any) {
	req := httptest.NewRequest("POST", "/api/provision/p1/backup/copy", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.router.ServeHTTP(w, req)
	out := map[string]any{}
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	return w, out
}

func TestCopyNowFilesARestoreIntoANewProject(t *testing.T) {
	h := newCopyHarness(t, backedUpProject(), roomyPlan{}, true)

	w, body := h.post(`{"name":"staging"}`)
	if w.Code != 200 {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	if body["mode"] != domain.RestoreModeNewProject || body["targetKind"] != domain.RestoreTargetNow || body["status"] != domain.RestoreStatusRunning {
		t.Errorf("job: %v", body)
	}
	job := <-h.ran
	if !job.Request.CopyNow || job.Request.NewProjectName != "staging" {
		t.Errorf("the step must be handed a copy of now: %+v", job.Request)
	}
	if job.NewProjectID == "" || job.NewProjectID == "p1" || job.Request.TargetProjectID != job.NewProjectID {
		t.Errorf("the copy's id is the platform's, never the source's: %+v", job)
	}
	if len(h.audit.entries) != 1 {
		t.Fatalf("audit entries: %d", len(h.audit.entries))
	}
	entry := h.audit.entries[0]
	if entry.Action != "database.copy" || entry.ProjectID != "p1" || entry.ResourceID != job.NewProjectID {
		t.Errorf("audit: %+v", entry)
	}
	var details map[string]any
	if err := json.Unmarshal([]byte(entry.Details), &details); err != nil {
		t.Fatalf("audit details: %v", err)
	}
	if details["point"] != "now" || details["jobId"] != job.ID || details["copyProjectId"] != job.NewProjectID {
		t.Errorf("audit details: %v", details)
	}
}

func TestCopyAtATimeCarriesTheTime(t *testing.T) {
	h := newCopyHarness(t, backedUpProject(), roomyPlan{}, true)

	w, body := h.post(`{"name":"before","point":"time","targetTime":"2026-10-07T12:00:00Z"}`)
	if w.Code != 200 {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	if body["targetKind"] != "time" {
		t.Errorf("job: %v", body)
	}
	job := <-h.ran
	if job.Request.CopyNow || job.Request.TargetTime == nil {
		t.Errorf("request: %+v", job.Request)
	}
	var details map[string]any
	_ = json.Unmarshal([]byte(h.audit.entries[0].Details), &details)
	if details["point"] != "time" || details["targetTime"] != "2026-10-07T12:00:00Z" {
		t.Errorf("audit details: %v", details)
	}
}

func TestCopyRefusals(t *testing.T) {
	noBackups := backedUpProject()
	noBackups.BackupEnabled = nil
	paused := backedUpProject()
	paused.Status = string(domain.StatusPaused)
	documentDB := backedUpProject()
	documentDB.DocumentDB = true

	cases := []struct {
		name    string
		project *domain.DatabaseInstance
		plan    service.OrgProjectCapacity
		body    string
		code    int
		says    string
	}{
		{"bad json", backedUpProject(), roomyPlan{}, `{`, 400, "invalid request"},
		{"no name", backedUpProject(), roomyPlan{}, `{}`, 400, "name is required"},
		{"bad point", backedUpProject(), roomyPlan{}, `{"name":"x","point":"soon"}`, 400, "point must be"},
		{"bad time", backedUpProject(), roomyPlan{}, `{"name":"x","point":"time","targetTime":"noon"}`, 400, "targetTime"},
		{"backups off", noBackups, roomyPlan{}, `{"name":"x"}`, 409, service.ErrCopyNeedsBackups.Error()},
		{"now while paused", paused, roomyPlan{}, `{"name":"x"}`, 409, service.ErrCopyNowNeedsRunning.Error()},
		{"documentdb not installed", documentDB, roomyPlan{}, `{"name":"x"}`, 409, service.ErrDocumentDBNotInstalled.Error()},
		{"no free slot", backedUpProject(), fullPlan{}, `{"name":"x"}`, 409, "project limit of 1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newCopyHarness(t, tc.project, tc.plan, true)
			w, body := h.post(tc.body)
			if w.Code != tc.code {
				t.Fatalf("status %d: %s", w.Code, w.Body.String())
			}
			if msg, _ := body["error"].(string); !strings.Contains(msg, tc.says) {
				t.Errorf("error %q, want it to say %q", msg, tc.says)
			}
			if len(h.jobs.jobs) != 0 || len(h.audit.entries) != 0 {
				t.Errorf("a refused copy files no job (%d) and audits nothing (%d)", len(h.jobs.jobs), len(h.audit.entries))
			}
		})
	}
}

// A copy is never in place, so the plan-limit refusal does not offer it.
func TestCopyAtThePlanLimitDoesNotOfferTheReplace(t *testing.T) {
	h := newCopyHarness(t, backedUpProject(), fullPlan{}, true)
	_, body := h.post(`{"name":"x"}`)
	if body["code"] != "project_limit_reached" {
		t.Errorf("code: %v", body["code"])
	}
	if msg, _ := body["error"].(string); strings.Contains(msg, "restore into this project") {
		t.Errorf("message offers an in-place restore: %q", msg)
	}
}

func TestCopyOfAnUnknownProjectIsNotFound(t *testing.T) {
	h := newCopyHarness(t, backedUpProject(), roomyPlan{}, true)
	req := httptest.NewRequest("POST", "/api/provision/nope/backup/copy", strings.NewReader(`{"name":"x"}`))
	w := httptest.NewRecorder()
	h.router.ServeHTTP(w, req)
	if w.Code != 404 {
		t.Fatalf("status %d", w.Code)
	}
}

// A copy is always a job; a platform without the orchestrator cannot run one.
func TestCopyNeedsTheRestoreOrchestrator(t *testing.T) {
	h := newCopyHarness(t, backedUpProject(), roomyPlan{}, false)
	w, _ := h.post(`{"name":"x"}`)
	if w.Code != 503 {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
}

func TestSecondCopyOfNowWhileTheFirstRunsIsRefused(t *testing.T) {
	h := newCopyHarness(t, backedUpProject(), roomyPlan{}, true)
	if w, _ := h.post(`{"name":"one"}`); w.Code != 200 {
		t.Fatalf("first: %d %s", w.Code, w.Body.String())
	}
	<-h.ran
	w, body := h.post(`{"name":"two"}`)
	if w.Code != 409 || !strings.Contains(body["error"].(string), "already running") {
		t.Fatalf("second: %d %v", w.Code, body)
	}
	if len(h.audit.entries) != 1 {
		t.Errorf("only the started copy is audited, got %d", len(h.audit.entries))
	}
}

func TestCopyErrorMappings(t *testing.T) {
	h := &BackupHandler{}
	cases := []struct {
		name  string
		write func(w *httptest.ResponseRecorder)
		code  int
		says  string
	}{
		{"backups off", func(w *httptest.ResponseRecorder) { writeCopyRefusal(w, service.ErrCopyNeedsBackups) }, 409, "turn on backups"},
		{"no store", func(w *httptest.ResponseRecorder) { writeCopyRefusal(w, service.ErrCopyNoBackupStore) }, 409, "no backup store"},
		{"now unsupported", func(w *httptest.ResponseRecorder) { writeCopyRefusal(w, service.ErrCopyNowUnsupported) }, 409, "latest backup or a time"},
		{"no adapter", func(w *httptest.ResponseRecorder) { writeCopyRefusal(w, service.ErrUnsupportedBackupMode) }, 409, "cannot be copied on this platform"},
		{"other check failure", func(w *httptest.ResponseRecorder) { writeCopyRefusal(w, errors.New("vault 10.0.0.5 down")) }, 500, ""},
		{"disk above plan", func(w *httptest.ResponseRecorder) { h.writeCopyCapacityError(w, service.ErrRestoreDiskAbovePlan) }, 409, "disk"},
		{"capacity unwired", func(w *httptest.ResponseRecorder) { h.writeCopyCapacityError(w, service.ErrOrgCapacityNotConfigured) }, 503, "not available"},
		{"tier unresolved", func(w *httptest.ResponseRecorder) { h.writeCopyCapacityError(w, service.ErrOrgTierUnresolved) }, 500, ""},
		{"other capacity failure", func(w *httptest.ResponseRecorder) { h.writeCopyCapacityError(w, errors.New("boom")) }, 500, ""},
		{"already running", func(w *httptest.ResponseRecorder) { h.writeCopyStartError(w, storage.ErrRestoreAlreadyRunning) }, 409, "already running"},
		{"start at the limit", func(w *httptest.ResponseRecorder) {
			h.writeCopyStartError(w, &service.OrgProjectLimitError{Limit: 2, Tier: domain.Standard})
		}, 409, "project limit of 2"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			tc.write(w)
			if w.Code != tc.code || !strings.Contains(w.Body.String(), tc.says) {
				t.Errorf("%d %s, want %d saying %q", w.Code, w.Body.String(), tc.code, tc.says)
			}
			if strings.Contains(w.Body.String(), "10.0.0.5") {
				t.Errorf("an internal detail reached the caller: %s", w.Body.String())
			}
		})
	}
}
