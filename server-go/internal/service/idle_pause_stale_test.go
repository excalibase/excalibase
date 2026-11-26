package service

import (
	"testing"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/storage"
)

// staleListing answers FindAll with the rows as the sweep first saw them,
// while every other read sees the project as it is now.
type staleListing struct {
	storage.InstanceStore
	listed []*domain.DatabaseInstance
}

func (s staleListing) FindAll() ([]*domain.DatabaseInstance, error) { return s.listed, nil }

// The customer resumed (or paused) the project after the sweep listed it. The
// retry must neither run nor count an attempt against the row.
func TestSelfHeal_AProjectThatMovedSinceTheListingIsLeftAlone(t *testing.T) {
	for _, tc := range []struct {
		listed, now domain.ProvisioningStage
	}{
		{domain.StatusPausing, "ACTIVE"},
		{domain.StatusResuming, "ACTIVE"},
		{domain.StatusResuming, domain.StatusPaused},
	} {
		f := newIdleFixture(t)
		f.stuckOn(t, "paid", domain.Standard, tc.listed)
		listed, _ := f.instances.FindByProjectID("paid")
		moved := *listed
		moved.Status = string(tc.now)
		if err := f.instances.Update(&moved); err != nil {
			t.Fatal(err)
		}
		sched := f.selfHealScheduler(true, false, nil)
		sched.instances = staleListing{InstanceStore: f.instances, listed: []*domain.DatabaseInstance{listed}}

		got := f.runOn(t, sched)

		now, _ := f.instances.FindByProjectID("paid")
		if len(got.Paused)+len(got.Resumed)+len(got.Failed) != 0 || f.pauser.attempts() != 0 || len(f.resumer.calls) != 0 ||
			now.Status != string(tc.now) || now.PauseAttempts != 0 {
			t.Fatalf("%s then %s: report %+v, pauser %d, resumer %v, row %s attempts %d",
				tc.listed, tc.now, got, f.pauser.attempts(), f.resumer.calls, now.Status, now.PauseAttempts)
		}
	}
}

func TestSelfHeal_BusyAndHealedRetriesAreNotFailures(t *testing.T) {
	for _, skipped := range []error{ErrProjectOperationRunning, storage.ErrProjectBusy, ErrNoLongerStuck} {
		f := newIdleFixture(t)
		f.stuckOn(t, "paid", domain.Standard, domain.StatusPausing)
		f.stuckOn(t, "resuming", domain.Standard, domain.StatusResuming)
		f.pauser.stuckErr, f.resumer.stuckErr = skipped, skipped
		alerts := NewAlertingService(t.TempDir())
		sched := f.selfHealScheduler(true, false, NewRetryEscalator(alerts, nil, 1))

		got := f.runOn(t, sched)

		if len(got.Failed) != 0 || len(openPlatformAlerts(t, alerts)) != 0 {
			t.Fatalf("%v counted as a failure: %+v", skipped, got)
		}
		if row, _ := f.instances.FindByProjectID("paid"); row.PauseAttempts != 0 {
			t.Fatalf("%v: attempt counted before the lease was held", skipped)
		}
	}
}
