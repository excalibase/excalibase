package storage

import (
	"errors"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/domain"
)

func liveRow() *domain.DatabaseInstance {
	return &domain.DatabaseInstance{ProjectID: "proj-1", OrgID: "org-1", Status: "ACTIVE"}
}

// The claim moves the row into DELETING and records the backup decision.
func TestApplyBeginDeletionRecordsTheBackupDecision(t *testing.T) {
	cases := []struct {
		name      string
		start     *domain.DatabaseInstance
		requested *bool
		want      bool
	}{
		{"no preference on a live project keeps the default", liveRow(), nil, false},
		{"confirmed purge is recorded", liveRow(), boolPtr(true), true},
		{"explicit keep on a live project", liveRow(), boolPtr(false), false},
		{"a bare retry inherits the recorded purge",
			&domain.DatabaseInstance{ProjectID: "proj-1", Status: string(domain.StatusDeleting), DeletionDeleteBackups: true},
			nil, true},
		{"a retry may still confirm a purge",
			&domain.DatabaseInstance{ProjectID: "proj-1", Status: string(domain.StatusDeleting)},
			boolPtr(true), true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ApplyBeginDeletion(tc.start, tc.requested)
			if err != nil {
				t.Fatalf("ApplyBeginDeletion: %v", err)
			}
			if got != tc.want || tc.start.DeletionDeleteBackups != tc.want {
				t.Errorf("effective = %v (row %v), want %v", got, tc.start.DeletionDeleteBackups, tc.want)
			}
			if tc.start.Status != string(domain.StatusDeleting) || tc.start.CurrentStage != domain.StatusDeleting {
				t.Errorf("status = %q/%q, want DELETING", tc.start.Status, tc.start.CurrentStage)
			}
			if tc.start.DeletionStep != "" || tc.start.DeletionError != "" {
				t.Error("a fresh claim must clear the previous attempt's note")
			}
		})
	}
}

// Asking to keep backups a running deletion was told to purge is refused.
func TestApplyBeginDeletionRefusesDowngradingAConfirmedPurge(t *testing.T) {
	inst := &domain.DatabaseInstance{
		ProjectID: "proj-1", Status: string(domain.StatusBackupsPendingDelete), DeletionDeleteBackups: true,
	}
	if _, err := ApplyBeginDeletion(inst, boolPtr(false)); !errors.Is(err, ErrBackupPurgeAlreadyConfirmed) {
		t.Fatalf("err = %v, want ErrBackupPurgeAlreadyConfirmed", err)
	}
	if !inst.DeletionDeleteBackups {
		t.Error("a refused claim must not change the recorded decision")
	}
}

// The narrow deletion write records the step and refuses live projects, so it
// can never be a second way into a deletion state.
func TestApplyDeletionFailure(t *testing.T) {
	inst := &domain.DatabaseInstance{ProjectID: "proj-1", Status: string(domain.StatusDeleting)}
	if err := ApplyDeletionFailure(inst, domain.StatusDeleting, "STEP", "why"); err != nil {
		t.Fatalf("ApplyDeletionFailure: %v", err)
	}
	if inst.DeletionStep != "STEP" || inst.DeletionError != "why" {
		t.Errorf("step/error = %q/%q", inst.DeletionStep, inst.DeletionError)
	}
	if inst.FailureReason != "" {
		t.Error("failure_reason belongs to the backups marker only")
	}

	if err := ApplyDeletionFailure(inst, domain.StatusBackupsPendingDelete, "STEP", "purge failed"); err != nil {
		t.Fatalf("ApplyDeletionFailure: %v", err)
	}
	if inst.FailureReason != "purge failed" {
		t.Errorf("failure reason = %q, want the purge failure", inst.FailureReason)
	}

	live := liveRow()
	if err := ApplyDeletionFailure(live, domain.StatusDeleting, "STEP", "why"); !errors.Is(err, ErrProjectNotDeleting) {
		t.Fatalf("err = %v, want ErrProjectNotDeleting", err)
	}
	if live.Status != "ACTIVE" {
		t.Errorf("a refused write changed the row: %q", live.Status)
	}
}

func TestCheckUpdatable(t *testing.T) {
	if err := CheckUpdatable(liveRow()); err != nil {
		t.Errorf("a live project must be updatable: %v", err)
	}
	for _, status := range []string{string(domain.StatusDeleting), string(domain.StatusBackupsPendingDelete)} {
		inst := &domain.DatabaseInstance{ProjectID: "proj-1", Status: status}
		if err := CheckUpdatable(inst); !errors.Is(err, ErrProjectDeleting) {
			t.Errorf("%s: err = %v, want ErrProjectDeleting", status, err)
		}
	}
}

// The filesystem store enforces the same door as the platform database.
func TestFileSystemStoreEnforcesTheDeletionDoor(t *testing.T) {
	store, err := NewFileSystemStore(t.TempDir())
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	inst := liveRow()
	if err := store.Create(inst); err != nil {
		t.Fatalf("Create: %v", err)
	}

	if _, err := store.BeginDeletion("missing", nil); !errors.Is(err, ErrProjectNotFound) {
		t.Errorf("BeginDeletion on a missing project = %v", err)
	}
	if err := store.RecordDeletionFailure("missing", domain.StatusDeleting, "S", "r"); !errors.Is(err, ErrProjectNotFound) {
		t.Errorf("RecordDeletionFailure on a missing project = %v", err)
	}
	if err := store.RecordDeletionFailure(inst.ProjectID, domain.StatusDeleting, "S", "r"); !errors.Is(err, ErrProjectNotDeleting) {
		t.Errorf("RecordDeletionFailure on a live project = %v", err)
	}

	effective, err := store.BeginDeletion(inst.ProjectID, boolPtr(true))
	if err != nil || !effective {
		t.Fatalf("BeginDeletion = %v, %v", effective, err)
	}
	if err := store.RecordDeletionFailure(inst.ProjectID, domain.StatusDeleting, "STEP", "why"); err != nil {
		t.Fatalf("RecordDeletionFailure: %v", err)
	}

	stale := liveRow()
	if err := store.Update(stale); !errors.Is(err, ErrProjectDeleting) {
		t.Fatalf("Update over a deleting row = %v, want ErrProjectDeleting", err)
	}
	got, _ := store.FindByProjectID(inst.ProjectID)
	if got.Status != string(domain.StatusDeleting) || got.DeletionStep != "STEP" || !got.DeletionDeleteBackups {
		t.Errorf("row was written over: %+v", got)
	}
	if effective, err := store.BeginDeletion(inst.ProjectID, boolPtr(false)); !errors.Is(err, ErrBackupPurgeAlreadyConfirmed) {
		t.Errorf("downgrade = %v, %v; want ErrBackupPurgeAlreadyConfirmed", effective, err)
	}
}

func boolPtr(b bool) *bool { return &b }
