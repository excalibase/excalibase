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

func ownedOrg(id, slug, owner string, tier domain.TierType) *domain.Org {
	return &domain.Org{ID: id, Name: id, Slug: slug, Tier: tier, OwnerID: owner}
}

// The org and its owner's membership land together: an org nobody owns can
// never be left behind.
func TestCreateOrgWithOwnerAddsTheOwnerMembership(t *testing.T) {
	store, userID := setupOrgTest(t)
	ctx := context.Background()

	if err := store.CreateOrgWithOwner(ctx, ownedOrg("org-a", "org-a", userID, domain.Free), 1); err != nil {
		t.Fatalf("CreateOrgWithOwner: %v", err)
	}
	member, err := store.GetOrgMember(ctx, "org-a", userID)
	if err != nil || member.Role != domain.OrgRoleOwner {
		t.Fatalf("owner membership = %+v, %v", member, err)
	}
}

func TestCreateOrgWithOwnerRefusesASecondFreeOrg(t *testing.T) {
	store, userID := setupOrgTest(t)
	ctx := context.Background()

	if err := store.CreateOrgWithOwner(ctx, ownedOrg("org-a", "org-a", userID, domain.Free), 1); err != nil {
		t.Fatalf("first: %v", err)
	}
	err := store.CreateOrgWithOwner(ctx, ownedOrg("org-b", "org-b", userID, domain.Free), 1)
	if !errors.Is(err, storage.ErrFreeOrgLimitReached) {
		t.Fatalf("second free org: got %v, want ErrFreeOrgLimitReached", err)
	}
	if org, _ := store.FindOrgByID(ctx, "org-b"); org != nil {
		t.Fatal("the refused org was written")
	}
}

// A paid org does not use the free allowance, and a free org that was
// upgraded gives it back.
func TestCreateOrgWithOwnerCountsOnlyFreeOrgs(t *testing.T) {
	store, userID := setupOrgTest(t)
	ctx := context.Background()

	if err := store.CreateOrgWithOwner(ctx, ownedOrg("org-a", "org-a", userID, domain.Free), 1); err != nil {
		t.Fatalf("first: %v", err)
	}
	upgraded, _ := store.FindOrgByID(ctx, "org-a")
	upgraded.Tier = domain.Standard
	if err := store.UpdateOrg(ctx, upgraded); err != nil {
		t.Fatalf("UpdateOrg: %v", err)
	}
	if err := store.CreateOrgWithOwner(ctx, ownedOrg("org-b", "org-b", userID, domain.Free), 1); err != nil {
		t.Fatalf("free org after the first was upgraded: %v", err)
	}
}

// The allowance belongs to the creator: handing the org's ownership to
// someone else and leaving does not free it.
func TestCreateOrgWithOwnerCountsOrgsTheUserCreatedAndLeft(t *testing.T) {
	store, userID := setupOrgTest(t)
	ctx := context.Background()

	if err := store.CreateOrgWithOwner(ctx, ownedOrg("org-a", "org-a", userID, domain.Free), 1); err != nil {
		t.Fatalf("first: %v", err)
	}
	if err := store.RemoveOrgMember(ctx, "org-a", userID); err != nil {
		t.Fatalf("leave: %v", err)
	}
	err := store.CreateOrgWithOwner(ctx, ownedOrg("org-b", "org-b", userID, domain.Free), 1)
	if !errors.Is(err, storage.ErrFreeOrgLimitReached) {
		t.Fatalf("got %v, want ErrFreeOrgLimitReached", err)
	}
}

func TestCreateOrgWithOwnerReportsATakenSlug(t *testing.T) {
	store, userID := setupOrgTest(t)
	ctx := context.Background()
	addInvitee(t, store, "other-user")

	if err := store.CreateOrgWithOwner(ctx, ownedOrg("org-a", "taken", userID, domain.Free), 1); err != nil {
		t.Fatalf("first: %v", err)
	}
	err := store.CreateOrgWithOwner(ctx, ownedOrg("org-b", "taken", "other-user", domain.Free), 1)
	if !errors.Is(err, storage.ErrOrgSlugTaken) {
		t.Fatalf("got %v, want ErrOrgSlugTaken", err)
	}
}

// Concurrent creates by one user are serialized: exactly one free org lands.
func TestCreateOrgWithOwnerHoldsTheCapUnderConcurrency(t *testing.T) {
	store, userID := setupOrgTest(t)
	ctx := context.Background()

	const attempts = 8
	var wg sync.WaitGroup
	results := make(chan error, attempts)
	for i := range attempts {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			id := fmt.Sprintf("org-%d", i)
			results <- store.CreateOrgWithOwner(ctx, ownedOrg(id, id, userID, domain.Free), 1)
		}(i)
	}
	wg.Wait()
	close(results)
	created := 0
	for err := range results {
		switch {
		case err == nil:
			created++
		case !errors.Is(err, storage.ErrFreeOrgLimitReached):
			t.Errorf("unexpected error: %v", err)
		}
	}
	if created != 1 {
		t.Fatalf("%d free orgs created, want exactly 1", created)
	}
}

func TestEnsurePersonalOrgCreatesOneOwnedOrg(t *testing.T) {
	store, userID := setupOrgTest(t)
	ctx := context.Background()

	created, err := store.EnsurePersonalOrg(ctx, ownedOrg("personal", "alice", userID, domain.Free))
	if err != nil || !created {
		t.Fatalf("EnsurePersonalOrg = %v, %v", created, err)
	}
	orgs, _ := store.FindOrgsByUser(ctx, userID)
	if len(orgs) != 1 || orgs[0].ID != "personal" || orgs[0].Tier != domain.Free {
		t.Fatalf("orgs = %+v", orgs)
	}
	if member, err := store.GetOrgMember(ctx, "personal", userID); err != nil || member.Role != domain.OrgRoleOwner {
		t.Fatalf("owner membership = %+v, %v", member, err)
	}

	again, err := store.EnsurePersonalOrg(ctx, ownedOrg("personal-2", "alice-2", userID, domain.Free))
	if err != nil || again {
		t.Fatalf("second ensure = %v, %v; want no new org", again, err)
	}
}

// A user who already created an org, of any plan, gets no extra one.
func TestEnsurePersonalOrgLeavesAUserWhoCreatedAnOrg(t *testing.T) {
	store, userID := setupOrgTest(t)
	ctx := context.Background()

	if err := store.CreateOrgWithOwner(ctx, ownedOrg("team", "team", userID, domain.Standard), 1); err != nil {
		t.Fatalf("CreateOrgWithOwner: %v", err)
	}
	created, err := store.EnsurePersonalOrg(ctx, ownedOrg("personal", "alice", userID, domain.Free))
	if err != nil || created {
		t.Fatalf("EnsurePersonalOrg = %v, %v; want no new org", created, err)
	}
}

// A user who is only a member of someone else's org still gets their own.
func TestEnsurePersonalOrgCreatesOneForAMemberOfAnotherOrg(t *testing.T) {
	store, userID := setupOrgTest(t)
	ctx := context.Background()
	addInvitee(t, store, "member-user")
	if err := store.CreateOrgWithOwner(ctx, ownedOrg("team", "team", userID, domain.Free), 1); err != nil {
		t.Fatalf("CreateOrgWithOwner: %v", err)
	}
	if err := store.AddOrgMember(ctx, &domain.OrgMember{OrgID: "team", UserID: "member-user", Role: "developer"}); err != nil {
		t.Fatalf("AddOrgMember: %v", err)
	}
	created, err := store.EnsurePersonalOrg(ctx, ownedOrg("personal", "member", "member-user", domain.Free))
	if err != nil || !created {
		t.Fatalf("EnsurePersonalOrg = %v, %v", created, err)
	}
}

func TestEnsurePersonalOrgCreatesExactlyOneUnderConcurrency(t *testing.T) {
	store, userID := setupOrgTest(t)
	ctx := context.Background()

	const attempts = 8
	var wg sync.WaitGroup
	for i := range attempts {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			id := fmt.Sprintf("personal-%d", i)
			if _, err := store.EnsurePersonalOrg(ctx, ownedOrg(id, id, userID, domain.Free)); err != nil {
				t.Errorf("EnsurePersonalOrg: %v", err)
			}
		}(i)
	}
	wg.Wait()
	orgs, _ := store.FindOrgsByUser(ctx, userID)
	if len(orgs) != 1 {
		t.Fatalf("%d personal orgs, want exactly 1", len(orgs))
	}
}

func TestEnsurePersonalOrgReportsATakenSlug(t *testing.T) {
	store, userID := setupOrgTest(t)
	ctx := context.Background()
	addInvitee(t, store, "other-user")
	if err := store.CreateOrgWithOwner(ctx, ownedOrg("team", "alice", "other-user", domain.Free), 1); err != nil {
		t.Fatalf("CreateOrgWithOwner: %v", err)
	}
	_, err := store.EnsurePersonalOrg(ctx, ownedOrg("personal", "alice", userID, domain.Free))
	if !errors.Is(err, storage.ErrOrgSlugTaken) {
		t.Fatalf("got %v, want ErrOrgSlugTaken", err)
	}
}
