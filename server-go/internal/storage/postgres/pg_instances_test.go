//go:build integration

package postgres

import (
	"errors"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/storage"
)

func instanceRow(projectID, orgID string) *domain.DatabaseInstance {
	port := 5432
	return &domain.DatabaseInstance{
		ProjectID: projectID, ProjectName: projectID, OrgID: orgID, OwnerID: "user-" + orgID,
		DBType: domain.PostgreSQL, Tier: domain.Free, Namespace: orgID + "-" + projectID,
		DeploymentMode: domain.ModeK8s,
		Host:           projectID + "-rw." + orgID + ".svc.cluster.local",
		Port:           &port, DatabaseName: "appdb",
		Username: projectID + "_admin", Password: projectID + "-password",
		Status: "ACTIVE",
	}
}

// Create is the only way a project row comes into existence, and a taken id
// is a conflict — never a silent rewrite of the row that holds it (EXC-415).
func TestInstances_CreateRejectsAnExistingProjectID(t *testing.T) {
	store := testStore(t)
	victim := instanceRow("proj-victim01", "org-victim")
	if err := store.Create(victim); err != nil {
		t.Fatalf("Create victim: %v", err)
	}

	err := store.Create(instanceRow("proj-victim01", "org-attacker"))
	if !errors.Is(err, storage.ErrProjectExists) {
		t.Fatalf("Create on a taken id: got %v, want ErrProjectExists", err)
	}

	got, err := store.FindByProjectID(victim.ProjectID)
	if err != nil || got == nil {
		t.Fatalf("victim row: %v", err)
	}
	if got.OrgID != "org-victim" || got.Host != victim.Host || got.Password != victim.Password {
		t.Errorf("victim row was rewritten: org=%q host=%q", got.OrgID, got.Host)
	}
}

// Update changes a project in place and can never move it to another org,
// whatever the caller puts on the struct.
func TestInstances_UpdateNeverMovesAProjectBetweenOrgs(t *testing.T) {
	store := testStore(t)
	victim := instanceRow("proj-victim02", "org-victim")
	if err := store.Create(victim); err != nil {
		t.Fatalf("Create: %v", err)
	}

	moved := instanceRow("proj-victim02", "org-attacker")
	moved.Status = "PAUSED"
	if err := store.Update(moved); err != nil {
		t.Fatalf("Update: %v", err)
	}

	got, _ := store.FindByProjectID("proj-victim02")
	if got.OrgID != "org-victim" {
		t.Errorf("org_id changed by Update: got %q, want %q", got.OrgID, "org-victim")
	}
	if got.Status != "PAUSED" {
		t.Errorf("Update must still persist mutable fields: status=%q", got.Status)
	}
}

// Updating a row that does not exist is an error, not a silent insert.
func TestInstances_UpdateMissingRowIsAnError(t *testing.T) {
	store := testStore(t)
	err := store.Update(instanceRow("proj-absent01", "org-x"))
	if !errors.Is(err, storage.ErrProjectNotFound) {
		t.Fatalf("Update on a missing row: got %v, want ErrProjectNotFound", err)
	}
	got, _ := store.FindByProjectID("proj-absent01")
	if got != nil {
		t.Error("a failed Update must not create the row")
	}
}
