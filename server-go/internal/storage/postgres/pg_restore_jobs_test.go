//go:build integration

package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
)

func TestRestoreJobs_RoundTrip(t *testing.T) {
	store := testStore(t)
	rs := NewRestoreJobs(store)
	ctx := context.Background()

	if err := rs.UpsertRestoreJob(ctx, &domain.RestoreJob{
		ID: "j1", SourceProjectID: "src", NewProjectID: "dst",
		Status: domain.RestoreStatusRunning, CurrentStep: "fetch", TargetKind: "time", TargetValue: "2026-05-01T00:00:00Z",
	}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	got, err := rs.FindRestoreJob(ctx, "src", "j1")
	if err != nil || got == nil {
		t.Fatalf("Find: %v", err)
	}
	if got.SourceProjectID != "src" || got.CurrentStep != "fetch" || got.TargetValue != "2026-05-01T00:00:00Z" {
		t.Errorf("roundtrip: %+v", got)
	}
}

func TestRestoreJobs_ListRunning(t *testing.T) {
	store := testStore(t)
	rs := NewRestoreJobs(store)
	ctx := context.Background()

	rs.UpsertRestoreJob(ctx, &domain.RestoreJob{ID: "r1", SourceProjectID: "s", NewProjectID: "d", Status: domain.RestoreStatusRunning, TargetKind: "latest"})
	rs.UpsertRestoreJob(ctx, &domain.RestoreJob{ID: "r2", SourceProjectID: "s", NewProjectID: "d2", Status: domain.RestoreStatusCompleted, TargetKind: "latest"})

	got, _ := rs.ListRunningRestoreJobs(ctx)
	if len(got) != 1 || got[0].ID != "r1" {
		t.Errorf("running: %+v", got)
	}
}

// The restore target id is generated (EXC-415), so the job row is the only
// place the new project's admin can learn it. Project-scoped lookup (EXC-399)
// must still resolve for BOTH named projects and for nobody else.
func TestRestoreJobs_GeneratedTargetIDStaysProjectScoped(t *testing.T) {
	store := testStore(t)
	rs := NewRestoreJobs(store)
	ctx := context.Background()

	const generatedTarget = "proj-gen4t9xq2p"
	if err := rs.UpsertRestoreJob(ctx, &domain.RestoreJob{
		ID: "j-scoped", SourceProjectID: "proj-source01", NewProjectID: generatedTarget,
		NewProjectName: "restored orders",
		Status:         domain.RestoreStatusRunning, TargetKind: "latest",
	}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	for _, caller := range []string{"proj-source01", generatedTarget} {
		got, err := rs.FindRestoreJob(ctx, caller, "j-scoped")
		if err != nil || got == nil {
			t.Fatalf("caller %q must see the job: %v", caller, err)
		}
		if got.NewProjectID != generatedTarget || got.NewProjectName != "restored orders" {
			t.Errorf("caller %q got: %+v", caller, got)
		}
	}

	got, err := rs.FindRestoreJob(ctx, "proj-stranger1", "j-scoped")
	if err != nil {
		t.Fatalf("Find as a third project: %v", err)
	}
	if got != nil {
		t.Errorf("a project named nowhere on the job must not reach it: %+v", got)
	}
}

// The conditional UPDATE is what makes multi-replica restores safe, so it is
// tested against the real table rather than only a fake.
func TestRestoreJobs_OnlyTheOwnerMayWriteARunningJob(t *testing.T) {
	store := testStore(t)
	rs := NewRestoreJobs(store)
	ctx := context.Background()

	if err := rs.UpsertRestoreJob(ctx, &domain.RestoreJob{
		ID: "own1", SourceProjectID: "src", NewProjectID: "dst",
		Status: domain.RestoreStatusRunning, Owner: "replica-a",
	}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	mine := domain.RestoreJob{ID: "own1", Status: domain.RestoreStatusRunning, CurrentStep: "recovering"}
	ok, err := rs.UpdateRunningRestoreJob(ctx, &mine, "replica-a")
	if err != nil || !ok {
		t.Fatalf("the owner's write must land: ok=%v err=%v", ok, err)
	}

	theirs := domain.RestoreJob{ID: "own1", Status: domain.RestoreStatusCompleted}
	ok, err = rs.UpdateRunningRestoreJob(ctx, &theirs, "replica-b")
	if err != nil {
		t.Fatalf("UpdateRunningRestoreJob: %v", err)
	}
	if ok {
		t.Error("a replica that does not own the job must not write it")
	}
	got, _ := rs.FindRestoreJob(ctx, "src", "own1")
	if got.Status != domain.RestoreStatusRunning || got.CurrentStep != "recovering" {
		t.Errorf("job was written by a non-owner: %+v", got)
	}
}

func TestRestoreJobs_TerminalJobsAreNotWritable(t *testing.T) {
	store := testStore(t)
	rs := NewRestoreJobs(store)
	ctx := context.Background()
	if err := rs.UpsertRestoreJob(ctx, &domain.RestoreJob{
		ID: "term1", SourceProjectID: "src", NewProjectID: "dst",
		Status: domain.RestoreStatusFailed, Owner: "replica-a",
	}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	back := domain.RestoreJob{ID: "term1", Status: domain.RestoreStatusCompleted}
	ok, err := rs.UpdateRunningRestoreJob(ctx, &back, "replica-a")
	if err != nil {
		t.Fatalf("UpdateRunningRestoreJob: %v", err)
	}
	if ok {
		t.Error("a FAILED job must not be writable back to COMPLETED")
	}
	if held, err := rs.HeartbeatRestoreJob(ctx, "term1", "replica-a"); err != nil || held {
		t.Errorf("a terminal job cannot be held: held=%v err=%v", held, err)
	}
}

func TestRestoreJobs_SweepSparesLiveOwnersAndFailsSilentOnes(t *testing.T) {
	store := testStore(t)
	rs := NewRestoreJobs(store)
	ctx := context.Background()
	for _, id := range []string{"live", "silent"} {
		if err := rs.UpsertRestoreJob(ctx, &domain.RestoreJob{
			ID: id, SourceProjectID: "src", NewProjectID: "dst-" + id,
			Status: domain.RestoreStatusRunning, Owner: "replica-a",
		}); err != nil {
			t.Fatalf("Upsert %s: %v", id, err)
		}
	}
	// "live" has just beaten; both were written moments ago, so a sweep with
	// a generous staleness bound must spare them.
	if held, err := rs.HeartbeatRestoreJob(ctx, "live", "replica-a"); err != nil || !held {
		t.Fatalf("heartbeat: held=%v err=%v", held, err)
	}
	failed, err := rs.FailAbandonedRestoreJobs(ctx, "replica-b", time.Hour, "abandoned")
	if err != nil {
		t.Fatalf("FailAbandonedRestoreJobs: %v", err)
	}
	if len(failed) != 0 {
		t.Errorf("fresh heartbeats must be spared, failed=%v", failed)
	}

	// With a zero staleness bound every heartbeat is already stale — except
	// for the sweeper's own jobs, which it knows are alive.
	failed, err = rs.FailAbandonedRestoreJobs(ctx, "replica-a", 0, "abandoned")
	if err != nil {
		t.Fatalf("FailAbandonedRestoreJobs: %v", err)
	}
	if len(failed) != 0 {
		t.Errorf("a replica must never fail its own jobs, failed=%v", failed)
	}
	failed, err = rs.FailAbandonedRestoreJobs(ctx, "replica-b", 0, "abandoned")
	if err != nil {
		t.Fatalf("FailAbandonedRestoreJobs: %v", err)
	}
	if len(failed) != 2 {
		t.Errorf("both silent jobs must be failed, failed=%v", failed)
	}
	got, _ := rs.FindRestoreJob(ctx, "src", "silent")
	if got.Status != domain.RestoreStatusFailed || got.FailureReason != "abandoned" {
		t.Errorf("swept job: %+v", got)
	}
}
