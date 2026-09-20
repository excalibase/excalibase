package fakestore

import (
	"errors"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/storage"
)

// The fake carries the real rule: the marker only lands on a project that is
// still being restored, and never changes its status.
func TestInstancesRecordRestoreInterrupted(t *testing.T) {
	instances := NewInstances()
	instances.Create(&domain.DatabaseInstance{
		ProjectID: "p1", OrgID: "o", Status: string(domain.StatusRestoring),
	})

	if err := instances.RecordRestoreInterrupted("p1", "RESTORE_INTERRUPTED", "delete and retry"); err != nil {
		t.Fatalf("RecordRestoreInterrupted: %v", err)
	}
	got, _ := instances.FindByProjectID("p1")
	if got.Status != string(domain.StatusRestoring) {
		t.Errorf("status: got %s, want RESTORING", got.Status)
	}
	if got.CurrentStep != "RESTORE_INTERRUPTED" || got.FailureReason != "delete and retry" {
		t.Errorf("marker: %+v", got)
	}
}

func TestInstancesRecordRestoreInterruptedRefusesOtherStates(t *testing.T) {
	instances := NewInstances()
	instances.Create(&domain.DatabaseInstance{ProjectID: "p1", OrgID: "o", Status: "ACTIVE"})

	if err := instances.RecordRestoreInterrupted("p1", "S", "r"); !errors.Is(err, storage.ErrProjectNotRestoring) {
		t.Errorf("live project: got %v, want ErrProjectNotRestoring", err)
	}
	if err := instances.RecordRestoreInterrupted("missing", "S", "r"); !errors.Is(err, storage.ErrProjectNotFound) {
		t.Errorf("missing project: got %v, want ErrProjectNotFound", err)
	}
}

// The fake counts pause attempts the same way the real stores do, including
// the one-way door — the sweep's backoff is only honest if the double is.
func TestInstancesRecordPauseAttempt(t *testing.T) {
	instances := NewInstances()
	instances.Create(&domain.DatabaseInstance{ProjectID: "p1", OrgID: "o", Status: "ACTIVE"})
	at := time.Unix(1_700_000_000, 0)

	for want := 1; want <= 2; want++ {
		got, err := instances.RecordPauseAttempt("p1", at)
		if err != nil {
			t.Fatalf("RecordPauseAttempt: %v", err)
		}
		if got != want {
			t.Errorf("attempts: got %d, want %d", got, want)
		}
	}
	inst, _ := instances.FindByProjectID("p1")
	if inst.PauseAttempts != 2 || inst.PauseLastAttemptAt == nil {
		t.Errorf("counter not stored: %+v", inst)
	}

	inst.Status = string(domain.StatusDeleting)
	instances.Items["p1"] = inst
	if _, err := instances.RecordPauseAttempt("p1", at); !errors.Is(err, storage.ErrProjectNotPausable) {
		t.Errorf("deleting project: got %v, want ErrProjectNotPausable", err)
	}
	if _, err := instances.RecordPauseAttempt("missing", at); !errors.Is(err, storage.ErrProjectNotFound) {
		t.Errorf("missing project: got %v, want ErrProjectNotFound", err)
	}
}
