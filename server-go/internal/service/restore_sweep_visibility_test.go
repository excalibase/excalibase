package service

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/storage"
)

// When a restore is abandoned, the job row says FAILED — but the user looks
// at the project, which sits in RESTORING saying nothing. The sweep must
// leave a reason on the project too, and a way forward.
func TestSweepRecordsTheInterruptionOnTheTargetProject(t *testing.T) {
	clock := &movableClock{t: time.Unix(8_000_000, 0)}
	jobs := newOwnedJobs(clock.now)
	instances, err := storage.NewFileSystemStore(t.TempDir())
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	if err := instances.Create(&domain.DatabaseInstance{
		ProjectID: "target-1", OrgID: "org", Status: string(domain.StatusRestoring),
	}); err != nil {
		t.Fatalf("seed target: %v", err)
	}
	ctx := context.Background()
	if err := jobs.UpsertRestoreJob(ctx, &domain.RestoreJob{
		ID: "j-abandoned", SourceProjectID: "src", NewProjectID: "target-1",
		Status: domain.RestoreStatusRunning, Owner: "dead-replica",
	}); err != nil {
		t.Fatalf("seed job: %v", err)
	}
	clock.advance(2 * defaultRestoreHeartbeatStale)

	orch := NewRestoreOrchestrator(RestoreOrchestratorConfig{
		Jobs: jobs, InstanceID: "live-replica", Now: clock.now, Instances: instances,
	})
	if err := orch.SweepAbandoned(ctx); err != nil {
		t.Fatalf("SweepAbandoned: %v", err)
	}

	target, err := instances.FindByProjectID("target-1")
	if err != nil || target == nil {
		t.Fatalf("the target project must not be deleted: %v", err)
	}
	if target.Status != string(domain.StatusRestoring) {
		t.Errorf("status: got %s, want RESTORING — the sweep must not revive or delete it", target.Status)
	}
	if target.FailureReason == "" {
		t.Fatal("the project must carry the reason its restore stopped")
	}
	if !strings.Contains(strings.ToLower(target.FailureReason), "delete") {
		t.Errorf("the reason must tell the user what to do, got %q", target.FailureReason)
	}
	if target.CurrentStep != restoreInterruptedStep {
		t.Errorf("step: got %q, want %q", target.CurrentStep, restoreInterruptedStep)
	}
}

// A target that already finished, or one that was never created, must not be
// disturbed by the sweep.
func TestSweepLeavesATargetThatIsNotRestoringAlone(t *testing.T) {
	clock := &movableClock{t: time.Unix(8_100_000, 0)}
	jobs := newOwnedJobs(clock.now)
	instances, _ := storage.NewFileSystemStore(t.TempDir())
	if err := instances.Create(&domain.DatabaseInstance{
		ProjectID: "target-live", OrgID: "org", Status: "ACTIVE",
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	ctx := context.Background()
	for id, target := range map[string]string{"j-live": "target-live", "j-missing": "never-made"} {
		if err := jobs.UpsertRestoreJob(ctx, &domain.RestoreJob{
			ID: id, SourceProjectID: "src", NewProjectID: target,
			Status: domain.RestoreStatusRunning, Owner: "dead-replica",
		}); err != nil {
			t.Fatalf("seed %s: %v", id, err)
		}
	}
	clock.advance(2 * defaultRestoreHeartbeatStale)

	orch := NewRestoreOrchestrator(RestoreOrchestratorConfig{
		Jobs: jobs, InstanceID: "live", Now: clock.now, Instances: instances,
	})
	if err := orch.SweepAbandoned(ctx); err != nil {
		t.Fatalf("SweepAbandoned: %v", err)
	}

	live, _ := instances.FindByProjectID("target-live")
	if live.Status != "ACTIVE" || live.FailureReason != "" {
		t.Errorf("a finished target must be untouched: %+v", live)
	}
}

// The sweep works without an instance store wired: the jobs are still failed.
func TestSweepWithoutAnInstanceStoreStillFailsJobs(t *testing.T) {
	clock := &movableClock{t: time.Unix(8_200_000, 0)}
	jobs := newOwnedJobs(clock.now)
	ctx := context.Background()
	if err := jobs.UpsertRestoreJob(ctx, &domain.RestoreJob{
		ID: "j-noinst", SourceProjectID: "src", NewProjectID: "t",
		Status: domain.RestoreStatusRunning, Owner: "dead",
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	clock.advance(2 * defaultRestoreHeartbeatStale)

	orch := ownedOrchestrator(jobs, "live", clock)
	if err := orch.SweepAbandoned(ctx); err != nil {
		t.Fatalf("SweepAbandoned: %v", err)
	}
	if got := jobs.status("j-noinst"); got != domain.RestoreStatusFailed {
		t.Errorf("status: got %s, want FAILED", got)
	}
}
