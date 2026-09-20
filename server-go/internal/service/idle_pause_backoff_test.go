package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
)

// stuckPausing records a project the way a failed idle pause leaves it.
func (f *idleFixture) stuckPausing(t *testing.T, id string, since time.Duration) {
	t.Helper()
	f.project(t, id, domain.Free, 8*day)
	inst, err := f.instances.FindByProjectID(id)
	if err != nil || inst == nil {
		t.Fatalf("load %s: %v", id, err)
	}
	inst.Status = string(domain.StatusPausing)
	inst.PauseReason = domain.PauseReasonIdle
	inst.CurrentStep = pauseStepBackup
	inst.UpdatedAt = &domain.FlexTime{Time: f.clock.Now().Add(-since)}
	if err := f.instances.Update(inst); err != nil {
		t.Fatalf("stick %s in PAUSING: %v", id, err)
	}
}

// newScheduler builds a second scheduler over the same stores — what a
// process restart or a leader change looks like to the data.
func (f *idleFixture) newScheduler() *IdlePauseScheduler {
	return NewIdlePauseScheduler(IdlePauseSchedulerConfig{
		Instances: f.instances,
		Activity:  f.activity,
		Tiers:     tierResolverForTest,
		Pauser:    f.pauser,
		Resumer:   f.resumer,
		Notifier:  f.notifier,
		Audit:     f.audit,
		Lock:      &fakeLeaderLock{},
		Now:       f.clock.Now,
	})
}

func (f *idleFixture) runOn(t *testing.T, s *IdlePauseScheduler) IdlePauseReport {
	t.Helper()
	report, err := s.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	return report
}

// The backoff lives on the project row, so a restart cannot reset it — the
// whole point is that a permanently failing pause does not file a Backup CR
// on every tick of every new process.
func TestPauseBackoffSurvivesARestart(t *testing.T) {
	f := newIdleFixture(t)
	f.stuckPausing(t, "stuck", time.Hour)
	f.pauser.err = errors.New("backup never completes")

	f.run(t)
	if f.pauser.attempts() != 1 {
		t.Fatalf("first sweep must attempt once, got %d", f.pauser.attempts())
	}
	inst, _ := f.instances.FindByProjectID("stuck")
	if inst.PauseAttempts != 1 || inst.PauseLastAttemptAt == nil {
		t.Fatalf("the attempt must be recorded on the row: %+v", inst)
	}

	// A different process takes over immediately. It must honour the
	// backoff it reads from the row rather than starting over.
	restarted := f.newScheduler()
	f.clock.Advance(time.Minute)
	f.runOn(t, restarted)
	if f.pauser.attempts() != 1 {
		t.Errorf("a restarted scheduler must not reset the backoff, attempts=%d", f.pauser.attempts())
	}

	// Once the backoff for one recorded attempt has elapsed, it tries again.
	f.clock.Advance(pauseRetryBackoffFor(1))
	f.runOn(t, restarted)
	if f.pauser.attempts() != 2 {
		t.Errorf("after the backoff a new process must retry, attempts=%d", f.pauser.attempts())
	}
}

// Each failure pushes the next attempt further out, up to a ceiling, so a
// project that can never pause costs a bounded number of attempts a day.
func TestPauseBackoffGrowsAndIsCapped(t *testing.T) {
	if pauseRetryBackoffFor(0) != pauseRetryBackoffBase {
		t.Errorf("first retry: got %v, want %v", pauseRetryBackoffFor(0), pauseRetryBackoffBase)
	}
	if pauseRetryBackoffFor(1) != 2*pauseRetryBackoffBase {
		t.Errorf("second retry: got %v, want %v", pauseRetryBackoffFor(1), 2*pauseRetryBackoffBase)
	}
	for _, attempts := range []int{10, 50, 1000} {
		if got := pauseRetryBackoffFor(attempts); got != pauseRetryBackoffCeiling {
			t.Errorf("attempts=%d: got %v, want the ceiling %v", attempts, got, pauseRetryBackoffCeiling)
		}
	}
	// Growth must be monotonic up to the ceiling.
	for i := 0; i < 8; i++ {
		if pauseRetryBackoffFor(i) > pauseRetryBackoffFor(i+1) {
			t.Errorf("backoff shrank between %d and %d", i, i+1)
		}
	}
}

// A pause that finally succeeds clears the record, so the next time this
// project goes idle it starts from a clean slate.
func TestASuccessfulPauseClearsTheBackoff(t *testing.T) {
	f := newIdleFixture(t)
	f.stuckPausing(t, "stuck", time.Hour)
	f.pauser.err = errors.New("transient")
	f.run(t)

	f.pauser.err = nil
	f.clock.Advance(pauseRetryBackoffFor(1))
	f.run(t)

	inst, _ := f.instances.FindByProjectID("stuck")
	if inst.Status != string(domain.StatusPaused) {
		t.Fatalf("status: got %s, want PAUSED", inst.Status)
	}
	if inst.PauseAttempts != 0 || inst.PauseLastAttemptAt != nil {
		t.Errorf("a successful pause must clear the backoff: attempts=%d last=%v",
			inst.PauseAttempts, inst.PauseLastAttemptAt)
	}
}

// The sweep must not touch a project the platform may not serve, whatever
// else its counters say.
func TestIdleSweepSkipsProjectsThatAreNotServable(t *testing.T) {
	for name, status := range map[string]string{
		"restoring": string(domain.StatusRestoring),
		"deleting":  string(domain.StatusDeleting),
	} {
		t.Run(name, func(t *testing.T) {
			f := newIdleFixture(t)
			f.project(t, "p1", domain.Free, 8*day)
			inst, _ := f.instances.FindByProjectID("p1")
			inst.Status = status
			if err := f.instances.Update(inst); err != nil {
				t.Fatalf("set %s: %v", status, err)
			}

			f.run(t)
			if f.pauser.attempts() != 0 {
				t.Errorf("a project the platform may not serve must be skipped, attempts=%d", f.pauser.attempts())
			}
		})
	}
}

// A project left in RESUMING has its database up and no CDC — a degraded
// tenant nobody is told about. Only PAUSING was being retried, so it sat
// there forever. It gets the same persisted backoff.
func TestTheSweepRetriesAStuckResume(t *testing.T) {
	f := newIdleFixture(t)
	f.project(t, "stuck-resume", domain.Free, 8*day)
	inst, _ := f.instances.FindByProjectID("stuck-resume")
	inst.Status = string(domain.StatusResuming)
	inst.CurrentStep = resumeStepReplication
	if err := f.instances.Update(inst); err != nil {
		t.Fatalf("stick in RESUMING: %v", err)
	}

	report := f.run(t)
	if len(f.resumer.calls) != 1 || f.resumer.calls[0] != "stuck-resume" {
		t.Fatalf("a stuck resume must be retried: %v", f.resumer.calls)
	}
	if len(report.Resumed) != 1 {
		t.Errorf("the report must name what it resumed: %+v", report)
	}

	// A retry that works settles the project and clears the backoff with it.
	after, _ := f.instances.FindByProjectID("stuck-resume")
	if after.Status != "ACTIVE" || after.PauseAttempts != 0 {
		t.Errorf("a converged resume must settle and clear the backoff: %+v", after)
	}
}

func TestTheSweepBacksOffStuckResumesToo(t *testing.T) {
	f := newIdleFixture(t)
	f.project(t, "stuck-resume", domain.Free, 8*day)
	inst, _ := f.instances.FindByProjectID("stuck-resume")
	inst.Status = string(domain.StatusResuming)
	inst.PauseAttempts = 1
	inst.PauseLastAttemptAt = &domain.FlexTime{Time: f.clock.Now()}
	if err := f.instances.Update(inst); err != nil {
		t.Fatalf("seed backoff: %v", err)
	}
	f.resumer.err = errors.New("cluster still not ready")

	f.run(t)
	if len(f.resumer.calls) != 0 {
		t.Fatalf("a resume inside its backoff must not be retried: %v", f.resumer.calls)
	}

	f.clock.Advance(pauseRetryBackoffFor(1))
	f.run(t)
	if len(f.resumer.calls) != 1 {
		t.Fatalf("after the backoff it must retry: %v", f.resumer.calls)
	}
	// A retry that fails is counted, so the next one waits longer.
	after, _ := f.instances.FindByProjectID("stuck-resume")
	if after.PauseAttempts != 2 {
		t.Errorf("attempts: got %d, want 2", after.PauseAttempts)
	}
}
