package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/storage"
)

func inPlaceRequest(projectID string) domain.RestoreRequest {
	return domain.RestoreRequest{Mode: domain.RestoreModeInPlace, ConfirmReplace: true, TargetProjectID: projectID}
}

func TestOrchestrator_InPlaceJobNamesTheProjectItself(t *testing.T) {
	jobs := newFakeJobs()
	orch := NewRestoreOrchestrator(RestoreOrchestratorConfig{Jobs: jobs})
	orch.SetSteps([]RestoreStep{{Name: domain.RestoreStepRestoring, Run: func(context.Context, *domain.RestoreJob) error { return nil }}})

	job, err := orch.Start(context.Background(), &domain.DatabaseInstance{ProjectID: "src"}, inPlaceRequest("src"))
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if job.Mode != domain.RestoreModeInPlace || job.NewProjectID != "src" {
		t.Fatalf("in-place job: %+v", job)
	}
	waitForJobStatus(t, jobs, "src", job.ID, domain.RestoreStatusCompleted)
}

func TestOrchestrator_InPlaceJobMustTargetTheSource(t *testing.T) {
	orch := NewRestoreOrchestrator(RestoreOrchestratorConfig{Jobs: newFakeJobs()})
	_, err := orch.Start(context.Background(), &domain.DatabaseInstance{ProjectID: "src"}, inPlaceRequest("someone-else"))
	if err == nil {
		t.Fatal("an in-place restore can only replace the project it was asked on")
	}
}

func TestOrchestrator_RefusesADuplicateWhileOneRuns(t *testing.T) {
	jobs := newFakeJobs()
	orch := NewRestoreOrchestrator(RestoreOrchestratorConfig{Jobs: jobs})
	release := make(chan struct{})
	defer close(release)
	orch.SetSteps([]RestoreStep{{Name: domain.RestoreStepRestoring, Run: func(ctx context.Context, _ *domain.RestoreJob) error {
		<-release
		return nil
	}}})
	ts := &domain.ZonedTime{Time: time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)}
	first := domain.RestoreRequest{NewProjectName: "copy", TargetProjectID: "dst-1", TargetTime: ts}
	if _, err := orch.Start(context.Background(), &domain.DatabaseInstance{ProjectID: "src"}, first); err != nil {
		t.Fatalf("first: %v", err)
	}
	second := first
	second.TargetProjectID = "dst-2"
	_, err := orch.Start(context.Background(), &domain.DatabaseInstance{ProjectID: "src"}, second)
	if !errors.Is(err, storage.ErrRestoreAlreadyRunning) {
		t.Fatalf("the same source and time while one runs must be refused, got %v", err)
	}
}

// The job is what Studio reads: its step and reason are sentences or known
// codes, never the name of an internal function or a wrapped Go error.
func TestOrchestrator_FailureReasonIsPublic(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want string
	}{
		{"known refusal keeps its words", fmt.Errorf("restore proj-x: %w", ErrRestoreTargetNotArchived), ErrRestoreTargetNotArchived.Error()},
		{"in-place outcome keeps its words", &InPlaceRestoreError{Public: "put back as it was"}, "put back as it was"},
		{"internal detail is not shown", errors.New(`exec "psql" in pod proj-x-postgres-1: exit 2`), restoreFailedReason},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			jobs := newFakeJobs()
			orch := NewRestoreOrchestrator(RestoreOrchestratorConfig{Jobs: jobs})
			orch.SetSteps([]RestoreStep{{Name: domain.RestoreStepRestoring, Run: func(context.Context, *domain.RestoreJob) error { return c.err }}})
			job, err := orch.Start(context.Background(), &domain.DatabaseInstance{ProjectID: "src"},
				domain.RestoreRequest{NewProjectName: "copy", TargetProjectID: "dst"})
			if err != nil {
				t.Fatal(err)
			}
			final := waitForJobStatus(t, jobs, "src", job.ID, domain.RestoreStatusFailed)
			if final.FailureReason != c.want {
				t.Errorf("reason %q, want %q", final.FailureReason, c.want)
			}
			if strings.Contains(final.FailureReason, "step") {
				t.Errorf("the step name is not the user's business: %q", final.FailureReason)
			}
		})
	}
}

func TestOrchestrator_StepReportsItsPhase(t *testing.T) {
	jobs := newFakeJobs()
	orch := NewRestoreOrchestrator(RestoreOrchestratorConfig{Jobs: jobs})
	seen := make(chan string, 1)
	proceed := make(chan struct{})
	orch.SetSteps([]RestoreStep{{Name: domain.RestoreStepRestoring, Run: func(ctx context.Context, j *domain.RestoreJob) error {
		ReportRestoreProgress(ctx, domain.RestoreStepSafetyBackup)
		seen <- j.ID
		<-proceed
		return nil
	}}})
	job, err := orch.Start(context.Background(), &domain.DatabaseInstance{ProjectID: "src"}, inPlaceRequest("src"))
	if err != nil {
		t.Fatal(err)
	}
	<-seen
	got, _ := jobs.FindRestoreJob(context.Background(), "src", job.ID)
	close(proceed)
	if got.CurrentStep != domain.RestoreStepSafetyBackup {
		t.Fatalf("current step %q, want %q", got.CurrentStep, domain.RestoreStepSafetyBackup)
	}
	waitForJobStatus(t, jobs, "src", job.ID, domain.RestoreStatusCompleted)
}

func TestReportRestoreProgressOutsideAJobIsHarmless(t *testing.T) {
	ReportRestoreProgress(context.Background(), domain.RestoreStepSafetyBackup)
}

// An abandoned in-place restore leaves the project itself RESTORING; the
// reason it is given tells the user to restore again, not to delete it.
func TestSweepOfAnInPlaceRestoreTellsTheUserToRestoreAgain(t *testing.T) {
	clock := &movableClock{t: time.Unix(9_000_000, 0)}
	jobs := newOwnedJobs(clock.now)
	instances, err := storage.NewFileSystemStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := instances.Create(&domain.DatabaseInstance{ProjectID: "src", OrgID: "org", Status: string(domain.StatusRestoring)}); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := jobs.UpsertRestoreJob(ctx, &domain.RestoreJob{
		ID: "j", SourceProjectID: "src", NewProjectID: "src", Mode: domain.RestoreModeInPlace,
		Status: domain.RestoreStatusRunning, Owner: "dead",
	}); err != nil {
		t.Fatal(err)
	}
	clock.advance(2 * defaultRestoreHeartbeatStale)
	orch := NewRestoreOrchestrator(RestoreOrchestratorConfig{Jobs: jobs, InstanceID: "live", Now: clock.now, Instances: instances})
	if err := orch.SweepAbandoned(ctx); err != nil {
		t.Fatal(err)
	}
	project, _ := instances.FindByProjectID("src")
	if project.Status != string(domain.StatusRestoring) {
		t.Fatalf("status %s", project.Status)
	}
	if !strings.Contains(project.FailureReason, "restore again") || strings.Contains(strings.ToLower(project.FailureReason), "delete this project") {
		t.Errorf("reason %q", project.FailureReason)
	}
}
