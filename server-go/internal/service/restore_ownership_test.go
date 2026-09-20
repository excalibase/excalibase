package service

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
)

// newOwnedJobs is a job store whose heartbeats age on a test-controlled
// clock, so ownership decisions are made without sleeping.
func newOwnedJobs(now func() time.Time) *fakeRestoreJobStore {
	jobs := newFakeJobs()
	jobs.clock = now
	return jobs
}

// movableClock lets a test age a heartbeat without sleeping. It is read by
// the driving goroutine, the heartbeat goroutine and the test at once — the
// same way the real clock is — so it is guarded.
type movableClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *movableClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *movableClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

func ownedOrchestrator(jobs *fakeRestoreJobStore, instance string, clock *movableClock) *RestoreOrchestrator {
	return NewRestoreOrchestrator(RestoreOrchestratorConfig{
		Jobs:       jobs,
		InstanceID: instance,
		Now:        clock.now,
	})
}

func startedJob(t *testing.T, orch *RestoreOrchestrator) *domain.RestoreJob {
	t.Helper()
	job, err := orch.Start(context.Background(), &domain.DatabaseInstance{ProjectID: "src"},
		domain.RestoreRequest{NewProjectName: "dst", TargetProjectID: "dst"})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	return job
}

// A rolling deploy restarts replica B while replica A is driving a restore.
// B's boot sweep must leave A's job alone — failing it would tell the caller
// the restore failed while it is still running, and a retry would then build
// the same project twice.
func TestBootSweepLeavesAJobADifferentLiveReplicaIsDriving(t *testing.T) {
	clock := &movableClock{t: time.Unix(1_000_000, 0)}
	jobs := newOwnedJobs(clock.now)
	replicaA := ownedOrchestrator(jobs, "replica-a", clock)

	release := make(chan struct{})
	done := make(chan struct{})
	replicaA.SetSteps([]RestoreStep{
		{Name: "long-recovery", Run: func(ctx context.Context, _ *domain.RestoreJob) error {
			<-release
			return nil
		}},
	})
	job := startedJob(t, replicaA)
	go func() { defer close(done); waitForJobStatus(t, jobs, "src", job.ID, domain.RestoreStatusCompleted) }()

	// B boots while A is mid-step and A's heartbeat is fresh.
	replicaB := ownedOrchestrator(jobs, "replica-b", clock)
	if err := replicaB.SweepAbandoned(context.Background()); err != nil {
		t.Fatalf("SweepAbandoned: %v", err)
	}
	if got := jobs.status(job.ID); got != domain.RestoreStatusRunning {
		t.Fatalf("a job a live replica is driving must be left alone, got %s", got)
	}

	close(release)
	<-done
	if got := jobs.status(job.ID); got != domain.RestoreStatusCompleted {
		t.Errorf("status: got %s, want COMPLETED", got)
	}
}

// Replica A dies mid-restore: nothing refreshes the heartbeat. Once enough
// heartbeats are missed, any other replica's sweep fails the job — without
// waiting for the next restart.
func TestSweepFailsAJobWhoseOwnerStoppedHeartbeating(t *testing.T) {
	clock := &movableClock{t: time.Unix(1_000_000, 0)}
	jobs := newOwnedJobs(clock.now)
	replicaA := ownedOrchestrator(jobs, "replica-a", clock)
	replicaA.SetSteps([]RestoreStep{{Name: "held", Run: func(ctx context.Context, _ *domain.RestoreJob) error {
		<-ctx.Done()
		return ctx.Err()
	}}})
	job := startedJob(t, replicaA)

	clock.advance(2 * defaultRestoreHeartbeatStale)
	replicaB := ownedOrchestrator(jobs, "replica-b", clock)
	if err := replicaB.SweepAbandoned(context.Background()); err != nil {
		t.Fatalf("SweepAbandoned: %v", err)
	}

	if got := jobs.status(job.ID); got != domain.RestoreStatusFailed {
		t.Fatalf("an abandoned job must be failed, got %s", got)
	}
}

// A driver whose job was taken from it must not write over the outcome the
// sweep recorded — the caller has already been told the restore failed.
func TestASweptDriversLateWriteIsRefused(t *testing.T) {
	clock := &movableClock{t: time.Unix(1_000_000, 0)}
	jobs := newOwnedJobs(clock.now)
	replicaA := ownedOrchestrator(jobs, "replica-a", clock)

	taken := make(chan struct{})
	stepCtx := make(chan context.Context, 1)
	replicaA.SetSteps([]RestoreStep{
		{Name: "slow", Run: func(ctx context.Context, _ *domain.RestoreJob) error {
			stepCtx <- ctx
			<-taken
			return nil
		}},
	})
	job := startedJob(t, replicaA)
	ctx := <-stepCtx

	// The job is swept out from under A.
	clock.advance(2 * defaultRestoreHeartbeatStale)
	replicaB := ownedOrchestrator(jobs, "replica-b", clock)
	if err := replicaB.SweepAbandoned(context.Background()); err != nil {
		t.Fatalf("SweepAbandoned: %v", err)
	}
	close(taken)

	// A's own writes from here must be refused, and the step's context must
	// be cancelled so its adapter stops and compensates.
	waitForJobStatus(t, jobs, "src", job.ID, domain.RestoreStatusFailed)
	if got := jobs.status(job.ID); got != domain.RestoreStatusFailed {
		t.Errorf("status: got %s, want the sweep's FAILED to stand", got)
	}
	select {
	case <-ctx.Done():
	case <-time.After(2 * time.Second):
		t.Error("the step's context must be cancelled when its job is taken")
	}
}

func TestTerminalRestoreJobsAreNeverOverwritten(t *testing.T) {
	clock := &movableClock{t: time.Unix(1_000_000, 0)}
	jobs := newOwnedJobs(clock.now)
	orch := ownedOrchestrator(jobs, "replica-a", clock)
	orch.SetSteps([]RestoreStep{{Name: "quick", Run: func(context.Context, *domain.RestoreJob) error { return nil }}})
	job := startedJob(t, orch)
	waitForJobStatus(t, jobs, "src", job.ID, domain.RestoreStatusCompleted)

	completed := *job
	completed.Status = domain.RestoreStatusRunning
	ok, err := jobs.UpdateRunningRestoreJob(context.Background(), &completed, "replica-a")
	if err != nil {
		t.Fatalf("UpdateRunningRestoreJob: %v", err)
	}
	if ok {
		t.Error("a COMPLETED job must not be writable back to RUNNING")
	}
	if got := jobs.status(job.ID); got != domain.RestoreStatusCompleted {
		t.Errorf("status: got %s, want COMPLETED", got)
	}
}

func TestInstanceIDsDifferBetweenProcesses(t *testing.T) {
	first, second := newInstanceID(), newInstanceID()
	if first == "" || first == second {
		t.Errorf("instance ids must be distinct: %q %q", first, second)
	}
	orch := NewRestoreOrchestrator(RestoreOrchestratorConfig{Jobs: newFakeJobs()})
	if orch.instance == "" || orch.heartbeat == 0 || orch.stale == 0 || orch.now == nil {
		t.Errorf("defaults not applied: %+v", orch)
	}
}

// The heartbeat is what keeps a long recovery from looking abandoned: the
// job's marker is refreshed while the step runs, so a peer sweeping mid-step
// sees a live owner however long the step takes.
func TestADrivenJobKeepsBeatingWhileItRuns(t *testing.T) {
	clock := &movableClock{t: time.Unix(5_000_000, 0)}
	jobs := newOwnedJobs(clock.now)
	jobs.beats = make(chan struct{}, 8)
	orch := NewRestoreOrchestrator(RestoreOrchestratorConfig{
		Jobs: jobs, InstanceID: "replica-a", Now: clock.now,
		Heartbeat: time.Millisecond,
	})
	release := make(chan struct{})
	started := make(chan struct{})
	orch.SetSteps([]RestoreStep{{Name: "slow", Run: func(context.Context, *domain.RestoreJob) error {
		close(started)
		<-release
		return nil
	}}})
	job := startedJob(t, orch)
	<-started

	// Age the clock past the staleness bound, then wait for a heartbeat that
	// was taken after the move — no sleeping, just the store telling us.
	clock.advance(2 * defaultRestoreHeartbeatStale)
	drainBeats(jobs.beats)
	select {
	case <-jobs.beats:
	case <-time.After(3 * time.Second):
		t.Fatal("the driver must keep beating while its step runs")
	}

	peer := ownedOrchestrator(jobs, "replica-b", clock)
	if err := peer.SweepAbandoned(context.Background()); err != nil {
		t.Fatalf("SweepAbandoned: %v", err)
	}
	if got := jobs.status(job.ID); got != domain.RestoreStatusRunning {
		t.Errorf("a beating job must survive a peer's sweep, got %s", got)
	}
	close(release)
	waitForJobStatus(t, jobs, "src", job.ID, domain.RestoreStatusCompleted)
}

// drainBeats discards heartbeats taken before the clock moved.
func drainBeats(beats chan struct{}) {
	for {
		select {
		case <-beats:
		default:
			return
		}
	}
}

func TestSweeperTickerStopsWhenCancelled(t *testing.T) {
	clock := &movableClock{t: time.Unix(6_000_000, 0)}
	jobs := newOwnedJobs(clock.now)
	ctx := context.Background()
	if err := jobs.UpsertRestoreJob(ctx, &domain.RestoreJob{
		ID: "orphan", SourceProjectID: "s", NewProjectID: "d",
		Status: domain.RestoreStatusRunning, Owner: "dead",
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	clock.advance(2 * defaultRestoreHeartbeatStale)
	orch := ownedOrchestrator(jobs, "me", clock)

	stop := orch.StartSweeper(ctx, leaderLeadership{}, time.Millisecond)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && jobs.status("orphan") != domain.RestoreStatusFailed {
		time.Sleep(2 * time.Millisecond)
	}
	stop()

	if got := jobs.status("orphan"); got != domain.RestoreStatusFailed {
		t.Errorf("the periodic sweeper must fail an orphan, got %s", got)
	}
}
