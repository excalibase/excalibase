package storage

import (
	"errors"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/domain"
)

func TestTheFileStoreAddsADatabaseOnlyToAProjectWithoutOne(t *testing.T) {
	store, err := NewFileSystemStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	inst := &domain.DatabaseInstance{ProjectID: "p1", OrgID: "o", Status: "ACTIVE", NoDatabase: true}
	if err := store.Create(inst); err != nil {
		t.Fatal(err)
	}
	inst.NoDatabase = false
	if err := store.Update(inst); err != nil {
		t.Fatal(err)
	}
	if got, _ := store.FindByProjectID("p1"); !got.NoDatabase {
		t.Fatal("a plain update gave the project a database")
	}
	if err := store.RecordDatabaseChoices(&domain.DatabaseInstance{ProjectID: "p1", DocumentDB: true, StorageSize: "5Gi"}, "PAUSED"); !errors.Is(err, ErrProjectStatusChanged) {
		t.Fatalf("got %v, want ErrProjectStatusChanged", err)
	}
	if err := store.RecordDatabaseChoices(&domain.DatabaseInstance{ProjectID: "p1", DocumentDB: true, StorageSize: "5Gi"}, "ACTIVE"); err != nil {
		t.Fatal(err)
	}
	if got, _ := store.FindByProjectID("p1"); !got.DocumentDB || got.StorageSize != "5Gi" {
		t.Fatalf("choices not recorded: %+v", got)
	}
	if err := store.MarkDatabaseAdded("p1"); err != nil {
		t.Fatal(err)
	}
	if got, _ := store.FindByProjectID("p1"); got.NoDatabase {
		t.Fatal("still no database after adding one")
	}
	if err := store.MarkDatabaseAdded("p1"); !errors.Is(err, ErrProjectHasDatabase) {
		t.Fatalf("got %v, want ErrProjectHasDatabase", err)
	}
}
