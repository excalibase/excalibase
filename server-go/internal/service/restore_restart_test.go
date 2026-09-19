package service

import (
	"context"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/domain"
)

// A restore runs in a goroutine owned by the process that started it. When
// that process goes away, nothing is driving the job any more — however
// recently it was updated. The boot sweep must say so rather than leave a
// RUNNING row that no clock will ever move.
func TestOrchestratorSweepFailsEveryRunningJobAtBoot(t *testing.T) {
	jobs := newFakeJobs()
	ctx := context.Background()
	fresh := &domain.RestoreJob{
		ID: "fresh", SourceProjectID: "s", NewProjectID: "d",
		Status: domain.RestoreStatusRunning, CurrentStep: "wait-for-cluster",
	}
	if err := jobs.UpsertRestoreJob(ctx, fresh); err != nil {
		t.Fatalf("seed job: %v", err)
	}

	orch := NewRestoreOrchestrator(RestoreOrchestratorConfig{Jobs: jobs})
	if err := orch.SweepStale(ctx); err != nil {
		t.Fatalf("SweepStale: %v", err)
	}

	got, err := jobs.FindRestoreJob(ctx, "s", "fresh")
	if err != nil {
		t.Fatalf("FindRestoreJob: %v", err)
	}
	if got.Status != domain.RestoreStatusFailed {
		t.Errorf("status: got %s, want FAILED — no process is driving this job", got.Status)
	}
	if got.FailureReason == "" {
		t.Error("an abandoned job must say why it failed")
	}
}

func TestOrchestratorSweepLeavesFinishedJobsAlone(t *testing.T) {
	jobs := newFakeJobs()
	ctx := context.Background()
	done := &domain.RestoreJob{
		ID: "done", SourceProjectID: "s", NewProjectID: "d",
		Status: domain.RestoreStatusCompleted,
	}
	if err := jobs.UpsertRestoreJob(ctx, done); err != nil {
		t.Fatalf("seed job: %v", err)
	}

	orch := NewRestoreOrchestrator(RestoreOrchestratorConfig{Jobs: jobs})
	if err := orch.SweepStale(ctx); err != nil {
		t.Fatalf("SweepStale: %v", err)
	}

	got, _ := jobs.FindRestoreJob(ctx, "s", "done")
	if got.Status != domain.RestoreStatusCompleted {
		t.Errorf("a finished job must survive the sweep, got %s", got.Status)
	}
}

func TestOrchestratorSweepReportsStoreFailure(t *testing.T) {
	orch := NewRestoreOrchestrator(RestoreOrchestratorConfig{Jobs: failingJobStore{}})
	if err := orch.SweepStale(context.Background()); err == nil {
		t.Fatal("a store that cannot be listed must be reported, not swallowed")
	}
}

// failingJobStore refuses every read so the sweep's error path is exercised.
type failingJobStore struct{}

func (failingJobStore) UpsertRestoreJob(context.Context, *domain.RestoreJob) error { return nil }

func (failingJobStore) FindRestoreJob(context.Context, string, string) (*domain.RestoreJob, error) {
	return nil, ErrRestoreJobNotFound
}

func (failingJobStore) ListRunningRestoreJobs(context.Context) ([]domain.RestoreJob, error) {
	return nil, context.DeadlineExceeded
}
