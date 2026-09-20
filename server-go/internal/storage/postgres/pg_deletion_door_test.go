//go:build integration

package postgres

import (
	"errors"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/storage"
)

// The one-way door is enforced by the UPDATE itself, so it holds for every
// caller and every control-plane replica — not only the ones that remember
// to check the status first.
func TestInstances_UpdateIsRefusedWhileDeleting(t *testing.T) {
	store := testStore(t)
	inst := instanceRow("proj-door0001", "org-door")
	if err := store.Create(inst); err != nil {
		t.Fatalf("Create: %v", err)
	}
	stale, err := store.FindByProjectID(inst.ProjectID)
	if err != nil {
		t.Fatalf("read: %v", err)
	}

	if _, err := store.BeginDeletion(inst.ProjectID, nil); err != nil {
		t.Fatalf("BeginDeletion: %v", err)
	}
	if err := store.RecordDeletionFailure(inst.ProjectID, domain.StatusDeleting,
		domain.DeletionStepDeleteResources, "namespace still present"); err != nil {
		t.Fatalf("RecordDeletionFailure: %v", err)
	}

	stale.Status = "ACTIVE"
	if err := store.Update(stale); !errors.Is(err, storage.ErrProjectDeleting) {
		t.Fatalf("Update = %v, want ErrProjectDeleting", err)
	}
	got, _ := store.FindByProjectID(inst.ProjectID)
	if got.Status != string(domain.StatusDeleting) || got.DeletionError == "" {
		t.Fatalf("row was written over: %+v", got)
	}
}

// The backup decision lives on the row: a retry that states no preference
// inherits it, and one that asks to keep them is refused.
func TestInstances_BeginDeletionKeepsTheRecordedBackupIntent(t *testing.T) {
	store := testStore(t)
	inst := instanceRow("proj-door0002", "org-door")
	if err := store.Create(inst); err != nil {
		t.Fatalf("Create: %v", err)
	}

	confirmed := true
	if effective, err := store.BeginDeletion(inst.ProjectID, &confirmed); err != nil || !effective {
		t.Fatalf("BeginDeletion = %v, %v; want true, nil", effective, err)
	}
	if effective, err := store.BeginDeletion(inst.ProjectID, nil); err != nil || !effective {
		t.Fatalf("bare retry = %v, %v; want the recorded true", effective, err)
	}
	keep := false
	if _, err := store.BeginDeletion(inst.ProjectID, &keep); !errors.Is(err, storage.ErrBackupPurgeAlreadyConfirmed) {
		t.Fatalf("downgrade = %v, want ErrBackupPurgeAlreadyConfirmed", err)
	}
}

// The narrow deletion writes are not a second way into a deletion state.
func TestInstances_RecordDeletionFailureRefusesALiveProject(t *testing.T) {
	store := testStore(t)
	inst := instanceRow("proj-door0003", "org-door")
	if err := store.Create(inst); err != nil {
		t.Fatalf("Create: %v", err)
	}
	err := store.RecordDeletionFailure(inst.ProjectID, domain.StatusDeleting, "STEP", "reason")
	if !errors.Is(err, storage.ErrProjectNotDeleting) {
		t.Fatalf("err = %v, want ErrProjectNotDeleting", err)
	}
	got, _ := store.FindByProjectID(inst.ProjectID)
	if got.Status != "ACTIVE" {
		t.Errorf("status = %q, want ACTIVE", got.Status)
	}
}

// A row that is gone and one a teardown owns are different refusals; the
// caller must be able to tell them apart.
func TestInstances_UpdateOnAMissingRowIsNotFound(t *testing.T) {
	store := testStore(t)
	inst := instanceRow("proj-door0004", "org-door")
	if err := store.Update(inst); !errors.Is(err, storage.ErrProjectNotFound) {
		t.Fatalf("Update = %v, want ErrProjectNotFound", err)
	}
	if _, err := store.BeginDeletion(inst.ProjectID, nil); !errors.Is(err, storage.ErrProjectNotFound) {
		t.Fatalf("BeginDeletion = %v, want ErrProjectNotFound", err)
	}
	if err := store.RecordDeletionFailure(inst.ProjectID, domain.StatusDeleting, "S", "r"); !errors.Is(err, storage.ErrProjectNotDeleting) {
		t.Fatalf("RecordDeletionFailure = %v, want ErrProjectNotDeleting", err)
	}
}

// The claim survives the round trip: what BeginDeletion recorded is what a
// later read sees, including the backup decision.
func TestInstances_DeletionStateRoundTrips(t *testing.T) {
	store := testStore(t)
	inst := instanceRow("proj-door0005", "org-door")
	if err := store.Create(inst); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := store.BeginDeletion(inst.ProjectID, storage.ApplyBeginDeletionIntent(true)); err != nil {
		t.Fatalf("BeginDeletion: %v", err)
	}
	if err := store.RecordDeletionFailure(inst.ProjectID, domain.StatusBackupsPendingDelete,
		domain.DeletionStepDeleteBackups, "r2 unavailable"); err != nil {
		t.Fatalf("RecordDeletionFailure: %v", err)
	}

	got, err := store.FindByProjectID(inst.ProjectID)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if got.Status != string(domain.StatusBackupsPendingDelete) ||
		got.DeletionStep != domain.DeletionStepDeleteBackups ||
		got.DeletionError != "r2 unavailable" ||
		got.FailureReason != "r2 unavailable" ||
		!got.DeletionDeleteBackups {
		t.Fatalf("round trip lost deletion state: %+v", got)
	}
}

// The restore-interrupted marker is the only thing the sweep writes on a
// target project, and the status predicate is its safety.
func TestInstances_RecordRestoreInterrupted(t *testing.T) {
	store := testStore(t)
	inst := instanceRow("proj-restint01", "org-door")
	if err := store.Create(inst); err != nil {
		t.Fatalf("Create: %v", err)
	}
	inst.Status = string(domain.StatusRestoring)
	if err := store.Update(inst); err != nil {
		t.Fatalf("set RESTORING: %v", err)
	}

	if err := store.RecordRestoreInterrupted(inst.ProjectID, "RESTORE_INTERRUPTED", "delete and try again"); err != nil {
		t.Fatalf("RecordRestoreInterrupted: %v", err)
	}
	got, _ := store.FindByProjectID(inst.ProjectID)
	if got.Status != string(domain.StatusRestoring) {
		t.Errorf("status: got %s, want RESTORING — the marker must not move it", got.Status)
	}
	if got.CurrentStep != "RESTORE_INTERRUPTED" || got.FailureReason != "delete and try again" {
		t.Errorf("marker: %+v", got)
	}
}

func TestInstances_RecordRestoreInterruptedRefusesANonRestoringProject(t *testing.T) {
	store := testStore(t)
	inst := instanceRow("proj-restint02", "org-door")
	if err := store.Create(inst); err != nil {
		t.Fatalf("Create: %v", err)
	}

	err := store.RecordRestoreInterrupted(inst.ProjectID, "S", "r")
	if !errors.Is(err, storage.ErrProjectNotRestoring) {
		t.Fatalf("err: got %v, want ErrProjectNotRestoring", err)
	}
}

// The retry counter must survive a restart, so it is written and read back
// through the real table — and the status predicate is what keeps it from
// touching a project the platform may not serve.
func TestInstances_RecordPauseAttemptPersistsAndRefusesNotServable(t *testing.T) {
	store := testStore(t)
	inst := instanceRow("proj-pauseatt1", "org-door")
	if err := store.Create(inst); err != nil {
		t.Fatalf("Create: %v", err)
	}

	now := time.Now().UTC().Truncate(time.Second)
	for want := 1; want <= 2; want++ {
		got, err := store.RecordPauseAttempt(inst.ProjectID, now)
		if err != nil {
			t.Fatalf("RecordPauseAttempt: %v", err)
		}
		if got != want {
			t.Errorf("attempts: got %d, want %d", got, want)
		}
	}
	reloaded, _ := store.FindByProjectID(inst.ProjectID)
	if reloaded.PauseAttempts != 2 || reloaded.PauseLastAttemptAt == nil {
		t.Fatalf("the backoff must survive a reload: %+v", reloaded)
	}

	// A general Update clears it the way a settled pause does.
	reloaded.PauseAttempts = 0
	reloaded.PauseLastAttemptAt = nil
	if err := store.Update(reloaded); err != nil {
		t.Fatalf("Update: %v", err)
	}
	cleared, _ := store.FindByProjectID(inst.ProjectID)
	if cleared.PauseAttempts != 0 || cleared.PauseLastAttemptAt != nil {
		t.Errorf("backoff not cleared: %+v", cleared)
	}

	// Once a teardown owns the row, the counter must not touch it.
	if _, err := store.BeginDeletion(inst.ProjectID, nil); err != nil {
		t.Fatalf("BeginDeletion: %v", err)
	}
	if _, err := store.RecordPauseAttempt(inst.ProjectID, now); !errors.Is(err, storage.ErrProjectNotPausable) {
		t.Errorf("deleting project: got %v, want ErrProjectNotPausable", err)
	}
}

// UpdateIfStatus is one statement, not a read then a write: the predicate is
// in the UPDATE, so a row that moves between the two cannot be written over.
func TestInstances_UpdateIfStatusPinsTheWrite(t *testing.T) {
	store := testStore(t)
	inst := instanceRow("proj-cond0001", "org-door")
	if err := store.Create(inst); err != nil {
		t.Fatalf("Create: %v", err)
	}
	stored, _ := store.FindByProjectID(inst.ProjectID)

	moving := stored.Clone()
	moving.Status = string(domain.StatusPausing)
	if err := store.UpdateIfStatus(moving, stored.Status); err != nil {
		t.Fatalf("UpdateIfStatus from the expected status: %v", err)
	}

	stale := moving.Clone()
	stale.Status = string(domain.StatusPaused)
	if err := store.UpdateIfStatus(stale, stored.Status); !errors.Is(err, storage.ErrProjectStatusChanged) {
		t.Errorf("a moved row: got %v, want ErrProjectStatusChanged", err)
	}
	if err := store.UpdateIfStatus(moving, ""); !errors.Is(err, storage.ErrProjectStatusChanged) {
		t.Errorf("no expected status: got %v, want ErrProjectStatusChanged", err)
	}
	after, _ := store.FindByProjectID(inst.ProjectID)
	if after.Status != string(domain.StatusPausing) {
		t.Errorf("status: got %s, want PAUSING", after.Status)
	}

	// The episode's backup round-trips too.
	after.PauseBackupID = "bk-episode"
	after.PauseBackupAt = &domain.FlexTime{Time: time.Now().UTC().Truncate(time.Second)}
	if err := store.Update(after); err != nil {
		t.Fatalf("Update: %v", err)
	}
	reloaded, _ := store.FindByProjectID(inst.ProjectID)
	if reloaded.PauseBackupID != "bk-episode" || reloaded.PauseBackupAt == nil {
		t.Errorf("episode backup not persisted: %+v", reloaded)
	}
}
