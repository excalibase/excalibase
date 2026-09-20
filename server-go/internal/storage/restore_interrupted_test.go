package storage

import (
	"errors"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
)

func restoringProject() *domain.DatabaseInstance {
	return &domain.DatabaseInstance{
		ProjectID: "target-x", OrgID: "org", Status: string(domain.StatusRestoring),
	}
}

// The marker must never move a project out of RESTORING: that status is what
// keeps it from being served and what lets it be deleted.
func TestRecordRestoreInterruptedKeepsTheProjectRestoring(t *testing.T) {
	store, err := NewFileSystemStore(t.TempDir())
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	if err := store.Create(restoringProject()); err != nil {
		t.Fatalf("create: %v", err)
	}

	if err := store.RecordRestoreInterrupted("target-x", "RESTORE_INTERRUPTED", "delete and try again"); err != nil {
		t.Fatalf("RecordRestoreInterrupted: %v", err)
	}

	got, _ := store.FindByProjectID("target-x")
	if got.Status != string(domain.StatusRestoring) {
		t.Errorf("status: got %s, want RESTORING", got.Status)
	}
	if got.CurrentStep != "RESTORE_INTERRUPTED" || got.FailureReason != "delete and try again" {
		t.Errorf("marker not stored: %+v", got)
	}
}

func TestRecordRestoreInterruptedRefusesAProjectThatIsNotRestoring(t *testing.T) {
	store, _ := NewFileSystemStore(t.TempDir())
	live := restoringProject()
	live.Status = "ACTIVE"
	if err := store.Create(live); err != nil {
		t.Fatalf("create: %v", err)
	}

	err := store.RecordRestoreInterrupted("target-x", "S", "r")
	if !errors.Is(err, ErrProjectNotRestoring) {
		t.Fatalf("err: got %v, want ErrProjectNotRestoring", err)
	}
	if err := store.RecordRestoreInterrupted("missing", "S", "r"); !errors.Is(err, ErrProjectNotFound) {
		t.Errorf("missing project: got %v, want ErrProjectNotFound", err)
	}
}

// The retry backoff lives on the row so it survives a restart, and the
// one-way door keeps it off a project the platform may not serve.
func TestRecordPauseAttemptCountsAndRefusesNotServable(t *testing.T) {
	store, err := NewFileSystemStore(t.TempDir())
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	live := restoringProject()
	live.Status = "ACTIVE"
	if err := store.Create(live); err != nil {
		t.Fatalf("create: %v", err)
	}
	now := time.Unix(1_700_000_000, 0)

	for want := 1; want <= 3; want++ {
		got, err := store.RecordPauseAttempt("target-x", now)
		if err != nil {
			t.Fatalf("RecordPauseAttempt: %v", err)
		}
		if got != want {
			t.Errorf("attempts: got %d, want %d", got, want)
		}
	}
	reloaded, _ := store.FindByProjectID("target-x")
	if reloaded.PauseAttempts != 3 || reloaded.PauseLastAttemptAt == nil {
		t.Errorf("the backoff must be on the row: %+v", reloaded)
	}

	stuck := reloaded.Clone()
	stuck.Status = string(domain.StatusRestoring)
	if err := store.Update(stuck); err != nil {
		t.Fatalf("update: %v", err)
	}
	if _, err := store.RecordPauseAttempt("target-x", now); !errors.Is(err, ErrProjectNotPausable) {
		t.Errorf("restoring project: got %v, want ErrProjectNotPausable", err)
	}
	if _, err := store.RecordPauseAttempt("missing", now); !errors.Is(err, ErrProjectNotFound) {
		t.Errorf("missing project: got %v, want ErrProjectNotFound", err)
	}
}

// UpdateIfStatus is the backstop behind the lifecycle lease: it writes only
// while the row still holds the status the operation last wrote.
func TestUpdateIfStatusPinsTheWriteToTheExpectedStatus(t *testing.T) {
	store, err := NewFileSystemStore(t.TempDir())
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	live := restoringProject()
	live.Status = "ACTIVE"
	if err := store.Create(live); err != nil {
		t.Fatalf("create: %v", err)
	}

	moving := live.Clone()
	moving.Status = string(domain.StatusPausing)
	if err := store.UpdateIfStatus(moving, "ACTIVE"); err != nil {
		t.Fatalf("UpdateIfStatus from the expected status: %v", err)
	}
	stored, _ := store.FindByProjectID("target-x")
	if stored.Status != string(domain.StatusPausing) {
		t.Fatalf("status: got %s, want PAUSING", stored.Status)
	}

	stale := stored.Clone()
	stale.Status = string(domain.StatusPaused)
	if err := store.UpdateIfStatus(stale, "ACTIVE"); !errors.Is(err, ErrProjectStatusChanged) {
		t.Errorf("a moved row: got %v, want ErrProjectStatusChanged", err)
	}
	if err := store.UpdateIfStatus(&domain.DatabaseInstance{ProjectID: "missing"}, "ACTIVE"); !errors.Is(err, ErrProjectNotFound) {
		t.Errorf("missing project: got %v, want ErrProjectNotFound", err)
	}

	// The one-way door still wins: a teardown's row is not writable at all.
	if _, err := store.BeginDeletion("target-x", nil); err != nil {
		t.Fatalf("BeginDeletion: %v", err)
	}
	deleting := stored.Clone()
	deleting.Status = "ACTIVE"
	if err := store.UpdateIfStatus(deleting, string(domain.StatusPausing)); !errors.Is(err, ErrProjectDeleting) {
		t.Errorf("deleting row: got %v, want ErrProjectDeleting", err)
	}
}
