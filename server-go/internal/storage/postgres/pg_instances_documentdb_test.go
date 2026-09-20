//go:build integration

package postgres

import (
	"testing"

	"github.com/excalibase/provisioning-poc/internal/domain"
)

// DocumentDB is chosen when the project is created and never afterwards: the
// cluster's preloaded libraries are fixed when it is provisioned, so a project
// cannot be turned into a DocumentDB project later. These tests pin that the
// storage layer, not a caller's good manners, is what makes it true — the
// column is absent from the UPDATE the way project_id and org_id are.

// The choice survives the round trip, so everything downstream — the cluster
// spec, the extension step, the API — can read it back off the project.
func TestInstances_DocumentDBChoiceIsStoredAtCreation(t *testing.T) {
	store := testStore(t)

	documentProject := instanceRow("proj-docstore1", "org-doc")
	documentProject.DocumentDB = true
	if err := store.Create(documentProject); err != nil {
		t.Fatalf("Create: %v", err)
	}
	plainProject := instanceRow("proj-docstore2", "org-doc")
	if err := store.Create(plainProject); err != nil {
		t.Fatalf("Create: %v", err)
	}

	got, err := store.FindByProjectID("proj-docstore1")
	if err != nil || got == nil {
		t.Fatalf("read the DocumentDB project: %v", err)
	}
	if !got.DocumentDB {
		t.Error("a project created with DocumentDB reads back without it")
	}

	got, err = store.FindByProjectID("proj-docstore2")
	if err != nil || got == nil {
		t.Fatalf("read the plain project: %v", err)
	}
	if got.DocumentDB {
		t.Error("a project created without DocumentDB reads back with it")
	}
}

// No update path may turn DocumentDB on. A project whose cluster never
// preloaded the extension's libraries cannot run it, so a row that claimed
// otherwise would have the API advertise a Mongo surface that does not exist.
func TestInstances_UpdateCannotTurnDocumentDBOn(t *testing.T) {
	store := testStore(t)
	inst := instanceRow("proj-docstore3", "org-doc")
	if err := store.Create(inst); err != nil {
		t.Fatalf("Create: %v", err)
	}

	inst.DocumentDB = true
	if err := store.Update(inst); err != nil {
		t.Fatalf("Update: %v", err)
	}

	got, err := store.FindByProjectID(inst.ProjectID)
	if err != nil || got == nil {
		t.Fatalf("read back: %v", err)
	}
	if got.DocumentDB {
		t.Error("an update turned DocumentDB on for a project that was not created with it")
	}
}

// Nor off. Turning it off in the row would leave the extension installed and
// the project's Mongo surface hidden — the record disagreeing with the
// database, which is the same failure in the other direction.
func TestInstances_UpdateCannotTurnDocumentDBOff(t *testing.T) {
	store := testStore(t)
	inst := instanceRow("proj-docstore4", "org-doc")
	inst.DocumentDB = true
	if err := store.Create(inst); err != nil {
		t.Fatalf("Create: %v", err)
	}

	inst.DocumentDB = false
	if err := store.Update(inst); err != nil {
		t.Fatalf("Update: %v", err)
	}

	got, err := store.FindByProjectID(inst.ProjectID)
	if err != nil || got == nil {
		t.Fatalf("read back: %v", err)
	}
	if !got.DocumentDB {
		t.Error("an update turned DocumentDB off for a project that was created with it")
	}
}

// FindAll feeds the project list Studio renders, so it has to carry the
// choice too — not only the single-project read.
func TestInstances_FindAllCarriesTheDocumentDBChoice(t *testing.T) {
	store := testStore(t)
	inst := instanceRow("proj-docstore5", "org-doc")
	inst.DocumentDB = true
	if err := store.Create(inst); err != nil {
		t.Fatalf("Create: %v", err)
	}

	all, err := store.FindAll()
	if err != nil {
		t.Fatalf("FindAll: %v", err)
	}
	var found *domain.DatabaseInstance
	for _, row := range all {
		if row.ProjectID == inst.ProjectID {
			found = row
		}
	}
	if found == nil {
		t.Fatalf("project %s is missing from FindAll", inst.ProjectID)
	}
	if !found.DocumentDB {
		t.Error("FindAll dropped the DocumentDB choice")
	}
}
