package service

import (
	"context"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
)

// A restore is driven by a goroutine in one process. When that process dies,
// nothing refreshes the job's heartbeat, and the next sweep — from any
// replica — records the truth instead of leaving a row that no clock will
// ever move.
func TestSweepFailsAJobAbandonedByADeadProcess(t *testing.T) {
	jobs := newFakeJobs()
	clock := &movableClock{t: time.Unix(2_000_000, 0)}
	jobs.clock = clock.now
	ctx := context.Background()
	if err := jobs.UpsertRestoreJob(ctx, &domain.RestoreJob{
		ID: "abandoned", SourceProjectID: "s", NewProjectID: "d",
		Status: domain.RestoreStatusRunning, CurrentStep: "wait-for-cluster",
		Owner: "dead-replica",
	}); err != nil {
		t.Fatalf("seed job: %v", err)
	}
	clock.advance(2 * defaultRestoreHeartbeatStale)

	orch := ownedOrchestrator(jobs, "live-replica", clock)
	if err := orch.SweepAbandoned(ctx); err != nil {
		t.Fatalf("SweepAbandoned: %v", err)
	}

	got, err := jobs.FindRestoreJob(ctx, "s", "abandoned")
	if err != nil {
		t.Fatalf("FindRestoreJob: %v", err)
	}
	if got.Status != domain.RestoreStatusFailed {
		t.Errorf("status: got %s, want FAILED — nothing is driving this job", got.Status)
	}
	if got.FailureReason == "" {
		t.Error("an abandoned job must say why it failed")
	}
}

func TestSweepLeavesFinishedJobsAlone(t *testing.T) {
	jobs := newFakeJobs()
	clock := &movableClock{t: time.Unix(2_000_000, 0)}
	jobs.clock = clock.now
	ctx := context.Background()
	if err := jobs.UpsertRestoreJob(ctx, &domain.RestoreJob{
		ID: "done", SourceProjectID: "s", NewProjectID: "d",
		Status: domain.RestoreStatusCompleted, Owner: "dead-replica",
	}); err != nil {
		t.Fatalf("seed job: %v", err)
	}
	clock.advance(2 * defaultRestoreHeartbeatStale)

	orch := ownedOrchestrator(jobs, "live-replica", clock)
	if err := orch.SweepAbandoned(ctx); err != nil {
		t.Fatalf("SweepAbandoned: %v", err)
	}

	got, _ := jobs.FindRestoreJob(ctx, "s", "done")
	if got.Status != domain.RestoreStatusCompleted {
		t.Errorf("a finished job must survive the sweep, got %s", got.Status)
	}
}

// SweepStale is what boot calls; it must be the same sweep, so a restarting
// replica cannot fail the restores its peers are driving.
func TestBootSweepIsTheOwnershipSweep(t *testing.T) {
	jobs := newFakeJobs()
	clock := &movableClock{t: time.Unix(2_000_000, 0)}
	jobs.clock = clock.now
	ctx := context.Background()
	if err := jobs.UpsertRestoreJob(ctx, &domain.RestoreJob{
		ID: "peer-job", SourceProjectID: "s", NewProjectID: "d",
		Status: domain.RestoreStatusRunning, Owner: "peer-replica",
	}); err != nil {
		t.Fatalf("seed job: %v", err)
	}

	orch := ownedOrchestrator(jobs, "booting-replica", clock)
	if err := orch.SweepStale(ctx); err != nil {
		t.Fatalf("SweepStale: %v", err)
	}

	got, _ := jobs.FindRestoreJob(ctx, "s", "peer-job")
	if got.Status != domain.RestoreStatusRunning {
		t.Errorf("a peer's live job must survive a boot sweep, got %s", got.Status)
	}
}

func TestSweepReportsStoreFailure(t *testing.T) {
	orch := NewRestoreOrchestrator(RestoreOrchestratorConfig{Jobs: failingJobStore{}})
	if err := orch.SweepAbandoned(context.Background()); err == nil {
		t.Fatal("a store that cannot be swept must be reported, not swallowed")
	}
}

// failingJobStore refuses the sweep so its error path is exercised.
type failingJobStore struct{}

func (failingJobStore) UpsertRestoreJob(context.Context, *domain.RestoreJob) error { return nil }

func (failingJobStore) FindRestoreJob(context.Context, string, string) (*domain.RestoreJob, error) {
	return nil, ErrRestoreJobNotFound
}

func (failingJobStore) ListRunningRestoreJobs(context.Context) ([]domain.RestoreJob, error) {
	return nil, nil
}

func (failingJobStore) UpdateRunningRestoreJob(context.Context, *domain.RestoreJob, string) (bool, error) {
	return false, nil
}

func (failingJobStore) HeartbeatRestoreJob(context.Context, string, string) (bool, error) {
	return false, nil
}

func (failingJobStore) FailAbandonedRestoreJobs(context.Context, string, time.Duration, string) ([]string, error) {
	return nil, context.DeadlineExceeded
}
