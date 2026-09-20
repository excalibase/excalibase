package storage

import (
	"errors"
	"testing"

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
