package service

import (
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
)

// resumingSince puts a project in RESUMING, its row last written age ago.
func (f *idleFixture) resumingSince(t *testing.T, id string, age time.Duration) {
	t.Helper()
	f.project(t, id, domain.Free, 8*day)
	inst, _ := f.instances.FindByProjectID(id)
	inst.Status = string(domain.StatusResuming)
	inst.UpdatedAt = &domain.FlexTime{Time: f.clock.Now().Add(-age)}
	if err := f.instances.Update(inst); err != nil {
		t.Fatalf("stick in RESUMING: %v", err)
	}
}

// EXC-590: a resume still running (a manual resume a minute ago) is not the
// sweep's to retry; only one that has not moved for a while is stuck.
func TestTheSweepLeavesAFreshResumeAlone(t *testing.T) {
	f := newIdleFixture(t)
	f.resumingSince(t, "fresh", time.Minute)
	f.run(t)
	if len(f.resumer.calls) != 0 {
		t.Fatalf("a resume in progress was retried: %v", f.resumer.calls)
	}
	after, _ := f.instances.FindByProjectID("fresh")
	if after.PauseAttempts != 0 {
		t.Errorf("a resume in progress was counted as a failed attempt: %d", after.PauseAttempts)
	}
	f.clock.Advance(DefaultStuckRetryAfter)
	f.run(t)
	if len(f.resumer.calls) != 1 {
		t.Fatalf("a resume stuck past %v was not retried: %v", DefaultStuckRetryAfter, f.resumer.calls)
	}
}

// The minimum time in RESUMING is the operator's to set.
func TestStuckRetryAfterIsConfigurable(t *testing.T) {
	f := newIdleFixture(t)
	f.scheduler.stuckRetryAfter = 2 * time.Minute
	f.resumingSince(t, "stuck", 3*time.Minute)
	f.run(t)
	if len(f.resumer.calls) != 1 {
		t.Fatalf("not retried after the configured minimum: %v", f.resumer.calls)
	}
	if got := NewIdlePauseScheduler(IdlePauseSchedulerConfig{}).stuckRetryAfter; got != DefaultStuckRetryAfter {
		t.Errorf("default %v, want %v", got, DefaultStuckRetryAfter)
	}
}

// EXC-590: a pause still running (a manual pause a minute ago) is not the
// sweep's to retry either; one whose row has not moved for a while is.
func TestTheSweepLeavesAFreshPauseAlone(t *testing.T) {
	f := newIdleFixture(t)
	f.stuckPausing(t, "fresh", time.Minute)
	f.run(t)
	if f.pauser.attempts() != 0 {
		t.Fatalf("a pause in progress was retried: %d", f.pauser.attempts())
	}
	after, _ := f.instances.FindByProjectID("fresh")
	if after.PauseAttempts != 0 {
		t.Errorf("a pause in progress was counted as a failed attempt: %d", after.PauseAttempts)
	}
	f.clock.Advance(DefaultStuckRetryAfter)
	f.run(t)
	if f.pauser.attempts() != 1 {
		t.Fatalf("a pause stuck past %v was not retried: %d", DefaultStuckRetryAfter, f.pauser.attempts())
	}
}
