//go:build integration

package postgres

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/storage"
)

// EXC-555: two owners demoting each other at once leave exactly one owner.
func TestConcurrentOwnerDemotionsKeepAnOwner(t *testing.T) {
	store, alice := setupOrgTest(t)
	ctx := context.Background()
	if err := store.CreateOrgWithOwner(ctx, ownedOrg("org-a", "org-a", alice, domain.Free), 0); err != nil {
		t.Fatalf("create: %v", err)
	}
	addInvitee(t, store, "bob")
	if err := store.AddOrgMember(ctx, &domain.OrgMember{OrgID: "org-a", UserID: "bob", Role: domain.OrgRoleOwner}); err != nil {
		t.Fatalf("second owner: %v", err)
	}

	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i, id := range []string{alice, "bob"} {
		wg.Add(1)
		go func(i int, id string) {
			defer wg.Done()
			errs[i] = store.UpdateOrgMemberRole(ctx, "org-a", id, domain.OrgRoleAdmin)
		}(i, id)
	}
	wg.Wait()

	refused := 0
	for _, err := range errs {
		if errors.Is(err, storage.ErrLastOwner) {
			refused++
		} else if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	}
	if refused != 1 {
		t.Fatalf("refusals = %d, want exactly 1 (errs %v)", refused, errs)
	}
}

func TestOrgMemberChangesNameAMissingMember(t *testing.T) {
	store, alice := setupOrgTest(t)
	ctx := context.Background()
	if err := store.CreateOrgWithOwner(ctx, ownedOrg("org-a", "org-a", alice, domain.Free), 0); err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := store.UpdateOrgMemberRole(ctx, "org-a", "ghost", domain.OrgRoleAdmin); !errors.Is(err, storage.ErrOrgMemberNotFound) {
		t.Errorf("update: got %v", err)
	}
	if err := store.RemoveOrgMember(ctx, "org-a", "ghost"); !errors.Is(err, storage.ErrOrgMemberNotFound) {
		t.Errorf("remove: got %v", err)
	}
	if err := store.RemoveOrgMember(ctx, "org-a", alice); !errors.Is(err, storage.ErrLastOwner) {
		t.Errorf("remove last owner: got %v", err)
	}
}

func TestAddProjectMemberReportsDuplicatesAndUnknownUsers(t *testing.T) {
	store, alice := setupOrgTest(t)
	ctx := context.Background()
	if err := store.CreateOrgWithOwner(ctx, ownedOrg("org-a", "org-a", alice, domain.Free), 0); err != nil {
		t.Fatalf("create: %v", err)
	}
	member := &domain.ProjectMember{ProjectID: "p1", OrgID: "org-a", UserID: alice, Role: domain.ProjectRoleViewer}
	if err := store.AddProjectMember(ctx, member); err != nil {
		t.Fatalf("first: %v", err)
	}
	if err := store.AddProjectMember(ctx, member); !errors.Is(err, storage.ErrProjectMemberExists) {
		t.Errorf("duplicate: got %v", err)
	}
	ghost := &domain.ProjectMember{ProjectID: "p1", OrgID: "org-a", UserID: "ghost", Role: domain.ProjectRoleViewer}
	if err := store.AddProjectMember(ctx, ghost); !errors.Is(err, storage.ErrUserNotFound) {
		t.Errorf("unknown user: got %v", err)
	}
}
