package storage

import (
	"errors"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/domain"
)

func TestFileSystemStore_RecordRollbackRetryIsPinnedToTheStatus(t *testing.T) {
	store, err := NewFileSystemStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	protected := true
	if err := store.Create(&domain.DatabaseInstance{ProjectID: "p", OrgID: "o", Status: "FAILED", DeletionProtection: &protected, RollbackLog: "old", FailureReason: "old"}); err != nil {
		t.Fatal(err)
	}

	if err := store.RecordRollbackRetry("p", "new", "new", "ACTIVE"); !errors.Is(err, ErrProjectStatusChanged) {
		t.Fatalf("a stale status: %v, want ErrProjectStatusChanged", err)
	}
	got, _ := store.FindByProjectID("p")
	if got.RollbackLog != "old" || got.FailureReason != "old" {
		t.Fatalf("a refused write landed: %+v", got)
	}

	if err := store.RecordRollbackRetry("p", "new", "why", "FAILED"); err != nil {
		t.Fatalf("RecordRollbackRetry: %v", err)
	}
	got, _ = store.FindByProjectID("p")
	if got.RollbackLog != "new" || got.FailureReason != "why" || got.DeletionProtection == nil || !*got.DeletionProtection {
		t.Fatalf("got %+v, want only the log and the reason changed", got)
	}
	if err := store.RecordRollbackRetry("missing", "x", "x", "FAILED"); !errors.Is(err, ErrProjectNotFound) {
		t.Fatalf("a missing row: %v", err)
	}
}

func TestFileSystemStore_RecordRollbackRetryRefusesARowBeingDeleted(t *testing.T) {
	store, err := NewFileSystemStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Create(&domain.DatabaseInstance{ProjectID: "p", OrgID: "o", Status: "FAILED"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.BeginDeletion("p", nil); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordRollbackRetry("p", "x", "x", string(domain.StatusDeleting)); !errors.Is(err, ErrProjectDeleting) {
		t.Fatalf("got %v, want ErrProjectDeleting", err)
	}
}
