package service

import (
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

// A failed idle pause leaves the project in PAUSING. The sweep only looks at
// ACTIVE projects, so without a retry that project never pauses again and
// never leaves the state either.
func TestIdlePauseRetriesAProjectStuckInPausing(t *testing.T) {
	f := newIdleFixture(t)
	f.stuckPausing(t, "stuck", idlePauseRetryBackoff+time.Minute)

	report := f.run(t)

	if len(report.Paused) != 1 || report.Paused[0] != "stuck" {
		t.Fatalf("a stuck pause must be retried: %+v", report)
	}
	if len(f.pauser.reasons) != 1 || f.pauser.reasons[0] != domain.PauseReasonIdle {
		t.Errorf("the retry must keep the original reason: %v", f.pauser.reasons)
	}
}

// A pause that keeps failing must not file a backup on every tick.
func TestIdlePauseBacksOffBetweenRetries(t *testing.T) {
	f := newIdleFixture(t)
	f.stuckPausing(t, "stuck", time.Minute)

	f.run(t)
	if f.pauser.count() != 0 {
		t.Fatalf("a pause that failed a minute ago must not be retried immediately")
	}

	f.clock.Advance(idlePauseRetryBackoff)
	f.run(t)
	if f.pauser.count() != 1 {
		t.Errorf("after the backoff the retry must run, got %d attempts", f.pauser.count())
	}
}

// A permanently failing pause is capped, so it cannot burn a Backup CR per
// tick for a whole day.
func TestIdlePauseCapsRetriesPerDay(t *testing.T) {
	f := newIdleFixture(t)
	f.stuckPausing(t, "stuck", idlePauseRetryBackoff+time.Minute)
	f.pauser.err = errors.New("backup never completes")

	for i := 0; i < idlePauseRetryCap+3; i++ {
		f.run(t)
		f.clock.Advance(idlePauseRetryBackoff + time.Minute)
	}

	if got := f.pauser.attempts(); got != idlePauseRetryCap {
		t.Errorf("attempts: got %d, want the daily cap of %d", got, idlePauseRetryCap)
	}

	// A new day lifts the cap: the project is still stuck and still worth
	// one more round of attempts.
	f.clock.Advance(24 * time.Hour)
	f.run(t)
	if got := f.pauser.attempts(); got != idlePauseRetryCap+1 {
		t.Errorf("attempts after a new day: got %d, want %d", got, idlePauseRetryCap+1)
	}
}

func TestIdlePauseDoesNotRetryAProjectThatIsNotStuck(t *testing.T) {
	f := newIdleFixture(t)
	f.project(t, "paused", domain.Free, 8*day)
	inst, _ := f.instances.FindByProjectID("paused")
	inst.Status = string(domain.StatusPaused)
	if err := f.instances.Update(inst); err != nil {
		t.Fatalf("update: %v", err)
	}

	f.run(t)
	if f.pauser.count() != 0 {
		t.Errorf("an already paused project must be left alone, got %d attempts", f.pauser.count())
	}
}
