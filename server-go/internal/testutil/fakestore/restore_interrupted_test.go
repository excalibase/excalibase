package fakestore

import (
	"errors"
	"testing"

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
