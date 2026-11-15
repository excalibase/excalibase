//go:build integration

package postgres

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/storage"
)

// enableOrgRequiredOnProjects turns on the guard the shared test helpers switch
// off, because most tests seed projects for orgs that were never created.
func enableOrgRequiredOnProjects(t *testing.T, store *Store) {
	t.Helper()
	if _, err := store.DB().Exec(`ALTER TABLE database_instances ENABLE TRIGGER database_instances_require_org`); err != nil {
		t.Fatalf("enable the org guard: %v", err)
	}
}

// A project cannot be created for an org that does not exist.
func TestCreateRefusesAProjectForAMissingOrg(t *testing.T) {
	store, _ := setupOrgTest(t)
	enableOrgRequiredOnProjects(t, store)

	if err := store.Create(instanceRow("proj-ghost001", "org-gone")); err == nil {
		t.Fatal("a project was created for an org with no row")
	}
	if inst, _ := store.FindByProjectID("proj-ghost001"); inst != nil {
		t.Fatal("the refused project left a row behind")
	}
}

// A new-project restore that is still running will create a project in the
// org, so the org cannot be deleted under it.
func TestDeleteOrgRefusesWhileANewProjectRestoreRuns(t *testing.T) {
	store, userID := setupOrgTest(t)
	ctx := context.Background()
	if err := store.CreateOrgWithOwner(ctx, ownedOrg("org-restore", "org-restore", userID, domain.Free), 1); err != nil {
		t.Fatalf("seed org: %v", err)
	}
	if _, err := store.DB().Exec(`INSERT INTO restore_jobs (id, source_project_id, new_project_id, status, mode, org_id)
		VALUES ('job-1', 'src', 'dst', 'RUNNING', 'new_project', 'org-restore')`); err != nil {
		t.Fatalf("seed restore job: %v", err)
	}

	if err := store.DeleteOrg(ctx, "org-restore"); !errors.Is(err, storage.ErrOrgHasProjects) {
		t.Fatalf("DeleteOrg = %v, want ErrOrgHasProjects", err)
	}
	if _, err := store.DB().Exec(`UPDATE restore_jobs SET status = 'COMPLETED' WHERE id = 'job-1'`); err != nil {
		t.Fatalf("finish restore job: %v", err)
	}
	if err := store.DeleteOrg(ctx, "org-restore"); err != nil {
		t.Fatalf("DeleteOrg once the restore is done: %v", err)
	}
}

// Whichever of the two wins, no project may be left pointing at a deleted org.
func TestDeleteOrgRacingProjectCreateNeverOrphans(t *testing.T) {
	store, userID := setupOrgTest(t)
	enableOrgRequiredOnProjects(t, store)
	ctx := context.Background()

	for round := range 15 {
		orgID := fmt.Sprintf("org-race-%d", round)
		projectID := fmt.Sprintf("proj-race%04d", round)
		if err := store.CreateOrg(ctx, &domain.Org{ID: orgID, Name: orgID, Slug: orgID, Tier: domain.Standard, OwnerID: userID}); err != nil {
			t.Fatalf("seed org: %v", err)
		}
		var wg sync.WaitGroup
		wg.Add(2)
		go func() { defer wg.Done(); _ = store.DeleteOrg(ctx, orgID) }()
		go func() { defer wg.Done(); _ = store.Create(instanceRow(projectID, orgID)) }()
		wg.Wait()

		org, _ := store.FindOrgByID(ctx, orgID)
		inst, _ := store.FindByProjectID(projectID)
		if inst != nil && org == nil {
			t.Fatalf("round %d: project %s is running in deleted org %s", round, projectID, orgID)
		}
	}
}

// Deleting any org, whatever its plan, does not hand the account a new personal one.
func TestDeletingAPaidPersonalOrgDoesNotIssueAnother(t *testing.T) {
	store, userID := setupOrgTest(t)
	ctx := context.Background()

	if created, err := store.EnsurePersonalOrg(ctx, ownedOrg("personal", "alice", userID, domain.Free)); err != nil || !created {
		t.Fatalf("first personal org = %v, %v", created, err)
	}
	upgraded, _ := store.FindOrgByID(ctx, "personal")
	upgraded.Tier = domain.Standard
	if err := store.UpdateOrg(ctx, upgraded); err != nil {
		t.Fatalf("upgrade: %v", err)
	}
	if err := store.DeleteOrg(ctx, "personal"); err != nil {
		t.Fatalf("DeleteOrg: %v", err)
	}
	if created, err := store.EnsurePersonalOrg(ctx, ownedOrg("personal-2", "alice-2", userID, domain.Free)); err != nil || created {
		t.Fatalf("sign-in after deleting a paid org = %v, %v; want no new org", created, err)
	}
}
