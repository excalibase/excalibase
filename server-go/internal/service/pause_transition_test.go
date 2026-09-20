package service

import (
	"context"
	"errors"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/storage"
)

// Belt and braces behind the lease: every status write a pause or resume
// makes is conditional on the status that operation itself last wrote. Even
// if leasing were bypassed, PAUSED can never land on a row somebody else
// moved — the transition stops instead.
func TestPauseRefusesToWritePausedOverAChangedRow(t *testing.T) {
	f := newObservedPause(t)
	// Something else moves the project while the workload is being stopped.
	f.pauser.onPause = func() {
		inst, _ := f.store.FindByProjectID(observedPauseProject)
		inst.Status = "ACTIVE"
		if err := f.store.Update(inst); err != nil {
			t.Fatalf("interfere: %v", err)
		}
	}

	err := f.pause(t)
	if !errors.Is(err, storage.ErrProjectStatusChanged) {
		t.Fatalf("err: got %v, want ErrProjectStatusChanged", err)
	}
	if got := f.reload(t).Status; got == string(domain.StatusPaused) {
		t.Error("PAUSED must never be written over a row this operation no longer owns")
	}
}

func TestResumeRefusesToWriteActiveOverAChangedRow(t *testing.T) {
	f := newObservedPause(t)
	if err := f.pause(t); err != nil {
		t.Fatalf("seed PAUSED: %v", err)
	}
	f.pauser.onResume = func() {
		inst, _ := f.store.FindByProjectID(observedPauseProject)
		inst.Status = string(domain.StatusPaused)
		if err := f.store.Update(inst); err != nil {
			t.Fatalf("interfere: %v", err)
		}
	}

	err := f.svc.Resume(context.Background(), observedPauseProject)
	if !errors.Is(err, storage.ErrProjectStatusChanged) {
		t.Fatalf("err: got %v, want ErrProjectStatusChanged", err)
	}
	if got := f.reload(t).Status; got == "ACTIVE" {
		t.Error("ACTIVE must never be written over a row this operation no longer owns")
	}
}

// The precondition is on the status the operation wrote, so an ordinary run
// — which nobody else touches — goes through untouched.
func TestConditionalWritesDoNotObstructAnUncontendedRun(t *testing.T) {
	f := newObservedPause(t)
	if err := f.pause(t); err != nil {
		t.Fatalf("Pause: %v", err)
	}
	if got := f.reload(t).Status; got != string(domain.StatusPaused) {
		t.Fatalf("status: got %s, want PAUSED", got)
	}
	if err := f.svc.Resume(context.Background(), observedPauseProject); err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if got := f.reload(t).Status; got != "ACTIVE" {
		t.Errorf("status: got %s, want ACTIVE", got)
	}
}

func TestUpdateIfStatusRefusesAMovedRow(t *testing.T) {
	f := newObservedPause(t)
	inst := f.reload(t)

	inst.Status = string(domain.StatusPaused)
	if err := f.store.UpdateIfStatus(inst, "ACTIVE"); err != nil {
		t.Fatalf("UpdateIfStatus from the expected status: %v", err)
	}
	moved := f.reload(t)
	moved.Status = string(domain.StatusResuming)
	if err := f.store.UpdateIfStatus(moved, "ACTIVE"); !errors.Is(err, storage.ErrProjectStatusChanged) {
		t.Fatalf("err: got %v, want ErrProjectStatusChanged", err)
	}
}
