//go:build integration

package postgres

import (
	"errors"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/storage"
)

func databaseLessRow(projectID, orgID string) *domain.DatabaseInstance {
	return &domain.DatabaseInstance{
		ProjectID: projectID, ProjectName: projectID, OrgID: orgID, OwnerID: "user-" + orgID,
		Tier: domain.Free, Namespace: orgID + "-" + projectID, DeploymentMode: domain.ModeK8s,
		Status: "ACTIVE", NoDatabase: true,
	}
}

func TestInstances_AProjectWithoutADatabaseReadsBackThatWay(t *testing.T) {
	store := testStore(t)
	if err := store.Create(databaseLessRow("proj-nodb0001", "org-nodb")); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := store.Create(instanceRow("proj-nodb0002", "org-nodb")); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if got, _ := store.FindByProjectID("proj-nodb0001"); got == nil || !got.NoDatabase {
		t.Fatalf("a project created without a database reads back with one: %+v", got)
	}
	if got, _ := store.FindByProjectID("proj-nodb0002"); got == nil || got.NoDatabase {
		t.Fatalf("a project created with a database reads back without one: %+v", got)
	}
}

// A plain update never flips the flag either way: only adding a database
// clears it, and nothing sets it again.
func TestInstances_UpdateLeavesTheDatabaseFlagAlone(t *testing.T) {
	store := testStore(t)
	inst := databaseLessRow("proj-nodb0003", "org-nodb")
	if err := store.Create(inst); err != nil {
		t.Fatalf("Create: %v", err)
	}
	inst.NoDatabase = false
	if err := store.Update(inst); err != nil {
		t.Fatalf("Update: %v", err)
	}
	if got, _ := store.FindByProjectID(inst.ProjectID); !got.NoDatabase {
		t.Fatal("an update gave a project a database it never got")
	}
}

func TestInstances_RecordingTheDatabaseChoicesNeedsAProjectWithoutOne(t *testing.T) {
	store := testStore(t)
	inst := databaseLessRow("proj-nodb0004", "org-nodb")
	if err := store.Create(inst); err != nil {
		t.Fatalf("Create: %v", err)
	}
	inst.DocumentDB = true
	inst.StorageClass = "fast"
	inst.StorageSize = "5Gi"
	inst.Parameters = map[string]string{"work_mem": "8MB"}
	if err := store.RecordDatabaseChoices(inst, "ACTIVE"); err != nil {
		t.Fatalf("RecordDatabaseChoices: %v", err)
	}
	got, _ := store.FindByProjectID(inst.ProjectID)
	if !got.DocumentDB || got.StorageClass != "fast" || got.StorageSize != "5Gi" || got.Parameters["work_mem"] != "8MB" {
		t.Fatalf("choices not recorded: %+v", got)
	}

	if err := store.MarkDatabaseAdded(inst.ProjectID); err != nil {
		t.Fatalf("MarkDatabaseAdded: %v", err)
	}
	if got, _ := store.FindByProjectID(inst.ProjectID); got.NoDatabase {
		t.Fatal("the project still has no database after one was added")
	}

	// Once the database exists its create-time choices are fixed again.
	inst.DocumentDB = false
	if err := store.RecordDatabaseChoices(inst, "ACTIVE"); !errors.Is(err, storage.ErrProjectHasDatabase) {
		t.Fatalf("rewriting a live database's choices: got %v, want ErrProjectHasDatabase", err)
	}
	if err := store.MarkDatabaseAdded(inst.ProjectID); !errors.Is(err, storage.ErrProjectHasDatabase) {
		t.Fatalf("adding a second database: got %v, want ErrProjectHasDatabase", err)
	}
}

func TestInstances_RecordingTheDatabaseChoicesIsPinnedToTheExpectedStatus(t *testing.T) {
	store := testStore(t)
	inst := databaseLessRow("proj-nodb0005", "org-nodb")
	if err := store.Create(inst); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := store.RecordDatabaseChoices(inst, "PAUSED"); !errors.Is(err, storage.ErrProjectStatusChanged) {
		t.Fatalf("got %v, want ErrProjectStatusChanged", err)
	}
}
