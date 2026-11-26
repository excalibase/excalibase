package service

import (
	"context"
	"errors"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/domain"
)

func (f *observedPauseFixture) setStatus(t *testing.T, status domain.ProvisioningStage) {
	t.Helper()
	inst := f.reload(t)
	inst.Status = string(status)
	if err := f.store.Update(inst); err != nil {
		t.Fatal(err)
	}
}

// The sweep read PAUSING or RESUMING up to a tick ago. Whatever the customer
// did since, a stuck retry must find the row still stuck under the lease.
func TestStuckRetry_PauseDoesNothingOnceTheProjectIsNoLongerPausing(t *testing.T) {
	for _, status := range []domain.ProvisioningStage{"ACTIVE", domain.StatusPaused, domain.StatusResuming} {
		t.Run(string(status), func(t *testing.T) {
			f := newObservedPause(t)
			f.setStatus(t, status)
			admitted := 0

			err := f.svc.PauseStuck(context.Background(), observedPauseProject, domain.PauseReasonIdle, func() error { admitted++; return nil })

			if !errors.Is(err, ErrNoLongerStuck) || admitted != 0 || len(f.pauser.log) != 0 {
				t.Fatalf("err %v, admitted %d, calls %v", err, admitted, f.pauser.log)
			}
			if got := f.reload(t); got.Status != string(status) || got.PauseReason != "" {
				t.Fatalf("row rewritten: %s reason %q", got.Status, got.PauseReason)
			}
		})
	}
}

func TestStuckRetry_ResumeDoesNothingOnceTheProjectIsNoLongerResuming(t *testing.T) {
	for _, status := range []domain.ProvisioningStage{"ACTIVE", domain.StatusPaused, domain.StatusPausing} {
		t.Run(string(status), func(t *testing.T) {
			f := newObservedPause(t)
			f.setStatus(t, status)
			admitted := 0

			err := f.svc.ResumeStuck(context.Background(), observedPauseProject, func() error { admitted++; return nil })

			if !errors.Is(err, ErrNoLongerStuck) || admitted != 0 || len(f.pauser.log) != 0 {
				t.Fatalf("err %v, admitted %d, calls %v", err, admitted, f.pauser.log)
			}
			if got := f.reload(t).Status; got != string(status) {
				t.Fatalf("status moved to %s", got)
			}
		})
	}
}

func TestStuckRetry_StillStuckRowsAreFinishedAndCounted(t *testing.T) {
	f := newObservedPause(t)
	f.setStatus(t, domain.StatusPausing)
	admitted := 0
	if err := f.svc.PauseStuck(context.Background(), observedPauseProject, domain.PauseReasonIdle, func() error { admitted++; return nil }); err != nil {
		t.Fatal(err)
	}
	if got := f.reload(t).Status; got != string(domain.StatusPaused) || admitted != 1 {
		t.Fatalf("status %s, admitted %d", got, admitted)
	}

	f.setStatus(t, domain.StatusResuming)
	if err := f.svc.ResumeStuck(context.Background(), observedPauseProject, func() error { admitted++; return nil }); err != nil {
		t.Fatal(err)
	}
	if got := f.reload(t).Status; got != "ACTIVE" || admitted != 2 {
		t.Fatalf("status %s, admitted %d", got, admitted)
	}
}

// Another operation holding the lease is not a failure and is not counted as an attempt.
func TestStuckRetry_BusyProjectIsNotCounted(t *testing.T) {
	f := newObservedPause(t)
	f.setStatus(t, domain.StatusPausing)
	release, claimed, err := f.svc.claimer.Claim(context.Background(), observedPauseProject, OperationRestore)
	if err != nil || !claimed {
		t.Fatalf("claim: %v %v", claimed, err)
	}
	defer release()
	admitted := 0

	err = f.svc.PauseStuck(context.Background(), observedPauseProject, domain.PauseReasonIdle, func() error { admitted++; return nil })

	if !errors.Is(err, ErrProjectOperationRunning) || admitted != 0 {
		t.Fatalf("err %v, admitted %d", err, admitted)
	}
}

// The sweep read ACTIVE up to a tick ago. An idle pause acts only on a row that
// is still ACTIVE under the lease, and only once the sweep's own check agrees.
func TestIdlePause_ServiceActsOnlyOnAnActiveRowTheSweepStillJudgesIdle(t *testing.T) {
	for _, status := range []domain.ProvisioningStage{domain.StatusPaused, domain.StatusPausing, domain.StatusResuming} {
		f := newObservedPause(t)
		f.setStatus(t, status)
		judged := 0

		err := f.svc.PauseIdle(context.Background(), observedPauseProject, domain.PauseReasonIdle, func() error { judged++; return nil })

		if !errors.Is(err, ErrNoLongerIdle) || judged != 0 || len(f.pauser.log) != 0 {
			t.Fatalf("%s: err %v, judged %d, calls %v", status, err, judged, f.pauser.log)
		}
	}

	f := newObservedPause(t)
	f.setStatus(t, "ACTIVE")
	err := f.svc.PauseIdle(context.Background(), observedPauseProject, domain.PauseReasonIdle, func() error { return ErrNoLongerIdle })
	if !errors.Is(err, ErrNoLongerIdle) || len(f.pauser.log) != 0 || f.reload(t).Status != "ACTIVE" {
		t.Fatalf("a project judged not idle was paused: %v %v", err, f.pauser.log)
	}

	if err := f.svc.PauseIdle(context.Background(), observedPauseProject, domain.PauseReasonIdle, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	if got := f.reload(t).Status; got != string(domain.StatusPaused) {
		t.Fatalf("status %s, want PAUSED", got)
	}
}
