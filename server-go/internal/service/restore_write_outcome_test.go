package service

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
)

// flakyJobs fails a configurable number of job writes with a transport-style
// error before behaving normally. An error is the absence of information —
// it says nothing about who owns the job.
type flakyJobs struct {
	*fakeRestoreJobStore
	mu          sync.Mutex
	failWrites  int
	writeCalls  int
	failForever bool
}

func newFlakyJobs(clock func() time.Time) *flakyJobs {
	return &flakyJobs{fakeRestoreJobStore: newOwnedJobs(clock)}
}

func (f *flakyJobs) UpdateRunningRestoreJob(ctx context.Context, j *domain.RestoreJob, owner string) (bool, error) {
	f.mu.Lock()
	f.writeCalls++
	refuse := f.failForever || f.failWrites > 0
	if f.failWrites > 0 {
		f.failWrites--
	}
	f.mu.Unlock()
	if refuse {
		return false, errors.New("platform db connection reset")
	}
	return f.fakeRestoreJobStore.UpdateRunningRestoreJob(ctx, j, owner)
}

func (f *flakyJobs) writes() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.writeCalls
}

// instantAfter fires every wait immediately, so a bounded retry spends its
// attempts without spending any time.
func instantAfter(time.Duration) <-chan time.Time {
	ch := make(chan time.Time, 1)
	ch <- time.Unix(0, 0)
	return ch
}

func flakyOrchestrator(jobs *flakyJobs, clock *movableClock) *RestoreOrchestrator {
	return NewRestoreOrchestrator(RestoreOrchestratorConfig{
		Jobs: jobs, InstanceID: "replica-a", Now: clock.now, After: instantAfter,
	})
}

// A platform-DB blip while claiming a step says nothing about ownership. The
// restore is healthy and still ours, so it must keep running — tearing down
// a customer's recovered database because a bookkeeping write failed is the
// worst thing this code could do.
func TestATransientWriteFailureDoesNotTearDownARestore(t *testing.T) {
	clock := &movableClock{t: time.Unix(7_000_000, 0)}
	jobs := newFlakyJobs(clock.now)
	orch := flakyOrchestrator(jobs, clock)

	// Every attempt at the step claim fails; the terminal write then works.
	jobs.mu.Lock()
	jobs.failWrites = defaultRestoreWriteAttempts
	jobs.mu.Unlock()

	cancelledDuringStep := make(chan bool, 1)
	orch.SetSteps([]RestoreStep{
		{Name: "recover", Run: func(ctx context.Context, _ *domain.RestoreJob) error {
			cancelledDuringStep <- ctx.Err() != nil
			return nil
		}},
	})
	job := startedJob(t, orch)

	if <-cancelledDuringStep {
		t.Error("an unreachable store must not cancel a restore this process still owns")
	}
	waitForJobStatus(t, jobs.fakeRestoreJobStore, "src", job.ID, domain.RestoreStatusCompleted)
}

// When the store cannot be reached for the terminal write, the driver stops
// quietly: it must not compensate (the restore may well have succeeded) and
// must not claim an outcome it could not record. The job stays RUNNING for
// the sweep to decide once the heartbeats stop.
func TestAnUnrecordableOutcomeLeavesTheJobForTheSweep(t *testing.T) {
	clock := &movableClock{t: time.Unix(7_100_000, 0)}
	jobs := newFlakyJobs(clock.now)
	orch := flakyOrchestrator(jobs, clock)

	done := make(chan struct{})
	orch.SetSteps([]RestoreStep{
		{Name: "recover", Run: func(context.Context, *domain.RestoreJob) error {
			jobs.mu.Lock()
			jobs.failForever = true
			jobs.mu.Unlock()
			return nil
		}},
	})
	job := startedJob(t, orch)
	go func() { defer close(done); waitForRunFinish(t, jobs, job.ID) }()
	<-done

	if got := jobs.status(job.ID); got != domain.RestoreStatusRunning {
		t.Errorf("status: got %s, want RUNNING — the outcome could not be recorded", got)
	}
}

// A 0-row write IS authoritative: the job was taken, so the step's context is
// cancelled and the adapter tears down what it built.
func TestAnAuthoritativeLossStopsAndCompensates(t *testing.T) {
	clock := &movableClock{t: time.Unix(7_200_000, 0)}
	jobs := newFlakyJobs(clock.now)
	orch := flakyOrchestrator(jobs, clock)

	swept := make(chan struct{})
	reachedFirst := make(chan struct{})
	secondRan := make(chan struct{}, 1)
	orch.SetSteps([]RestoreStep{
		{Name: "first", Run: func(context.Context, *domain.RestoreJob) error {
			close(reachedFirst)
			<-swept
			return nil
		}},
		{Name: "second", Run: func(context.Context, *domain.RestoreJob) error {
			secondRan <- struct{}{}
			return nil
		}},
	})
	job := startedJob(t, orch)
	<-reachedFirst

	// Take the job while the first step is in flight, then let it finish so
	// the driver tries to claim the second step and is refused.
	clock.advance(2 * defaultRestoreHeartbeatStale)
	peer := ownedOrchestrator(jobs.fakeRestoreJobStore, "replica-b", clock)
	if err := peer.SweepAbandoned(context.Background()); err != nil {
		t.Fatalf("SweepAbandoned: %v", err)
	}
	close(swept)

	select {
	case <-secondRan:
		t.Fatal("a driver whose job was taken must stop, not run the next step")
	case <-time.After(300 * time.Millisecond):
	}
	if got := jobs.status(job.ID); got != domain.RestoreStatusFailed {
		t.Errorf("status: got %s, want the sweep's FAILED to stand", got)
	}
}

// A blip that clears must not cost the restore anything: the write retries
// and the run carries on.
func TestAWriteRetriesUntilItLands(t *testing.T) {
	clock := &movableClock{t: time.Unix(7_300_000, 0)}
	jobs := newFlakyJobs(clock.now)
	orch := flakyOrchestrator(jobs, clock)
	orch.SetSteps([]RestoreStep{{Name: "recover", Run: func(context.Context, *domain.RestoreJob) error { return nil }}})

	jobs.mu.Lock()
	jobs.failWrites = 1
	jobs.mu.Unlock()
	job := startedJob(t, orch)

	waitForJobStatus(t, jobs.fakeRestoreJobStore, "src", job.ID, domain.RestoreStatusCompleted)
	if jobs.writes() < 2 {
		t.Errorf("the write must have been retried, calls=%d", jobs.writes())
	}
}

// A step that panics must take its own restore down, not the whole control
// plane: every other project's provisioning, pausing and teardown runs in
// this process too.
func TestAPanickingStepFailsOnlyItsOwnJob(t *testing.T) {
	clock := &movableClock{t: time.Unix(7_400_000, 0)}
	jobs := newOwnedJobs(clock.now)
	orch := ownedOrchestrator(jobs, "replica-a", clock)
	orch.SetSteps([]RestoreStep{
		{Name: "explodes", Run: func(context.Context, *domain.RestoreJob) error {
			panic("nil map write in the adapter")
		}},
	})

	job := startedJob(t, orch)
	got := waitForJobStatus(t, jobs, "src", job.ID, domain.RestoreStatusFailed)

	if !strings.Contains(strings.ToLower(got.FailureReason), "unexpected") {
		t.Errorf("failure reason must be a fixed sentence, got %q", got.FailureReason)
	}
	if strings.Contains(got.FailureReason, "nil map") {
		t.Errorf("the panic's internals must stay in the log, got %q", got.FailureReason)
	}
}

// waitForRunFinish waits until the driving goroutine has stopped writing.
func waitForRunFinish(t *testing.T, jobs *flakyJobs, id string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	last := -1
	for time.Now().Before(deadline) {
		calls := jobs.writes()
		if calls == last && calls > 0 {
			return
		}
		last = calls
		time.Sleep(20 * time.Millisecond)
	}
}

// A heartbeat the store cannot answer is not evidence of anything either:
// the run carries on and the next beat decides.
func TestAHeartbeatErrorDoesNotStopTheRun(t *testing.T) {
	clock := &movableClock{t: time.Unix(7_500_000, 0)}
	jobs := newOwnedJobs(clock.now)
	jobs.beatErrs = 2
	jobs.beats = make(chan struct{}, 8)
	orch := NewRestoreOrchestrator(RestoreOrchestratorConfig{
		Jobs: jobs, InstanceID: "replica-a", Now: clock.now, Heartbeat: time.Millisecond,
	})
	release := make(chan struct{})
	started := make(chan struct{})
	orch.SetSteps([]RestoreStep{{Name: "held", Run: func(context.Context, *domain.RestoreJob) error {
		close(started)
		<-release
		return nil
	}}})
	job := startedJob(t, orch)
	<-started

	select {
	case <-jobs.beats:
	case <-time.After(3 * time.Second):
		t.Fatal("the heartbeat must recover after a store error")
	}
	close(release)
	waitForJobStatus(t, jobs, "src", job.ID, domain.RestoreStatusCompleted)
}
