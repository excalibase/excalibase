package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
)

// selfHealScheduler is the fixture's scheduler with the self-heal switches the
// test sets.
func (f *idleFixture) selfHealScheduler(selfHeal, idleOff bool, escalator *RetryEscalator) *IdlePauseScheduler {
	return NewIdlePauseScheduler(IdlePauseSchedulerConfig{
		Instances: f.instances, Activity: f.activity, Tiers: tierResolverForTest,
		Pauser: f.pauser, Resumer: f.resumer, Notifier: f.notifier, Audit: f.audit,
		Lock: &fakeLeaderLock{}, Now: f.clock.Now,
		SelfHeal: selfHeal, IdleDisabled: idleOff, Escalator: escalator,
	})
}

// unreadableActivity fails the one read an idle-off sweep must never make.
type unreadableActivity struct{ *fakeActivityStore }

func (unreadableActivity) ListProjectActivity(context.Context) (map[string]domain.ProjectActivity, error) {
	return nil, errors.New("activity must not be read")
}

func (f *idleFixture) stuckOn(t *testing.T, id string, tier domain.TierType, status domain.ProvisioningStage) {
	t.Helper()
	f.project(t, id, tier, 8*day)
	inst, _ := f.instances.FindByProjectID(id)
	inst.Status = string(status)
	inst.UpdatedAt = &domain.FlexTime{Time: f.clock.Now().Add(-time.Hour)}
	if err := f.instances.Update(inst); err != nil {
		t.Fatal(err)
	}
}

// A plan with auto-pause off used to skip the stuck sweep entirely, leaving a
// failed manual pause in PAUSING for good.
func TestSelfHeal_RetriesAStuckPauseOnAPlanWithoutAutoPause(t *testing.T) {
	f := newIdleFixture(t)
	f.stuckOn(t, "paid", domain.Standard, domain.StatusPausing)

	if got := f.runOn(t, f.selfHealScheduler(false, false, nil)); len(got.Paused) != 0 {
		t.Fatalf("flag off must keep the old behaviour, paused %v", got.Paused)
	}
	if got := f.runOn(t, f.selfHealScheduler(true, false, nil)); len(got.Paused) != 1 || got.Paused[0] != "paid" {
		t.Fatalf("paused %v, want the stuck paid project", got.Paused)
	}
}

func TestSelfHeal_RetriesAStuckResumeOnAPlanWithoutAutoPause(t *testing.T) {
	f := newIdleFixture(t)
	f.stuckOn(t, "paid", domain.Standard, domain.StatusResuming)

	if got := f.runOn(t, f.selfHealScheduler(false, false, nil)); len(got.Resumed) != 0 {
		t.Fatalf("flag off must keep the old behaviour, resumed %v", got.Resumed)
	}
	if got := f.runOn(t, f.selfHealScheduler(true, false, nil)); len(got.Resumed) != 1 {
		t.Fatalf("resumed %v, want the stuck project", got.Resumed)
	}
}

// With auto-pause off the sweep still runs for stuck operations, and never
// pauses an idle project or reads the activity table.
func TestSelfHeal_IdleOffStillRetriesButNeverPausesAnIdleProject(t *testing.T) {
	f := newIdleFixture(t)
	f.project(t, "idle", domain.Free, 30*day)
	f.stuckOn(t, "stuck", domain.Free, domain.StatusPausing)
	sched := f.selfHealScheduler(true, true, nil)
	sched.activity = unreadableActivity{f.activity}

	got := f.runOn(t, sched)

	if len(got.Paused) != 1 || got.Paused[0] != "stuck" || len(got.Warned) != 0 {
		t.Fatalf("report %+v, want only the stuck project paused", got)
	}
}

func TestSelfHeal_AlertsStaffAfterTheLimitAndClosesWhenItHeals(t *testing.T) {
	f := newIdleFixture(t)
	f.stuckOn(t, "paid", domain.Standard, domain.StatusPausing)
	f.pauser.err = errors.New("hibernation stuck")
	alerts := NewAlertingService(t.TempDir())
	notifier := &mailCountingNotifier{}
	sched := f.selfHealScheduler(true, false, NewRetryEscalator(alerts, notifier, 2))

	for range 2 {
		f.runOn(t, sched)
		f.clock.Advance(7 * time.Hour)
	}
	if open := openPlatformAlerts(t, alerts); !open[StuckRetryMetricPrefix+"paid"] || notifier.raised != 1 {
		t.Fatalf("after 2 failed attempts: open %v, mails %d", open, notifier.raised)
	}

	f.pauser.err = nil
	f.runOn(t, sched)
	if open := openPlatformAlerts(t, alerts); len(open) != 0 || notifier.cleared != 1 {
		t.Fatalf("after it healed: open %v, resolved mails %d", open, notifier.cleared)
	}
}

func TestSelfHeal_BelowTheLimitStaffAreNotAlerted(t *testing.T) {
	f := newIdleFixture(t)
	f.stuckOn(t, "paid", domain.Standard, domain.StatusResuming)
	f.resumer.err = errors.New("not ready")
	alerts := NewAlertingService(t.TempDir())

	f.runOn(t, f.selfHealScheduler(true, false, NewRetryEscalator(alerts, nil, 3)))

	if open := openPlatformAlerts(t, alerts); len(open) != 0 {
		t.Fatalf("alert after 1 of 3 attempts: %v", open)
	}
}
