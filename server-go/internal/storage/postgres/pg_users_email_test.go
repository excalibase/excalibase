//go:build integration

package postgres

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/testutil"
)

const (
	migrationBeforeEmailFold = 66
	migrationEmailFold       = 67
)

func humanWithEmail(id, address string) *domain.User {
	return &domain.User{
		ID: id, Username: "u-" + id, Email: address,
		PasswordHash: testutil.FixturePasswordHash(), Role: "user", Active: true,
	}
}

func TestFindUserByEmailIgnoresCase(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	if err := store.CreateUser(ctx, humanWithEmail("dev", "Dev@X.example.com")); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	found, err := store.FindUserByEmail(ctx, "dev@x.example.COM")
	if err != nil || found == nil || found.ID != "dev" {
		t.Fatalf("lookup with other case: %+v %v", found, err)
	}
	if found.Email != "Dev@X.example.com" {
		t.Errorf("stored address was rewritten to %q; the original is kept for display", found.Email)
	}
}

func TestAnAddressDifferingOnlyByCaseIsRefused(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	if err := store.CreateUser(ctx, humanWithEmail("first", "Dev@X.example.com")); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	if err := store.CreateUser(ctx, humanWithEmail("second", "dev@x.example.com")); err == nil {
		t.Fatal("a second human account took an address differing only by case")
	}
}

func TestEmailFoldMigrationRefusesExistingCaseDuplicates(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	if err := store.m.Migrate(migrationBeforeEmailFold); err != nil {
		t.Fatalf("migrate down to %d: %v", migrationBeforeEmailFold, err)
	}
	for _, u := range []*domain.User{humanWithEmail("a", "Dev@X.example.com"), humanWithEmail("b", "dev@x.example.com")} {
		if err := store.CreateUser(ctx, u); err != nil {
			t.Fatalf("seed %s: %v", u.ID, err)
		}
	}
	err := store.m.Migrate(migrationEmailFold)
	if err == nil {
		t.Fatal("the migration indexed lower(email) over two accounts sharing an address")
	}
	if !strings.Contains(err.Error(), "differ only by case") || !strings.Contains(err.Error(), "dev@x.example.com") {
		t.Fatalf("the failure does not name the duplicate address: %v", err)
	}
}

func TestEmailFoldMigrationDownAllowsCaseVariantsAgain(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	if err := store.m.Migrate(migrationBeforeEmailFold); err != nil {
		t.Fatalf("migrate down to %d: %v", migrationBeforeEmailFold, err)
	}
	if err := store.CreateUser(ctx, humanWithEmail("a", "Dev@X.example.com")); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := store.CreateUser(ctx, humanWithEmail("b", "dev@x.example.com")); err != nil {
		t.Fatalf("down migration left the case-folded index: %v", err)
	}
}

func TestReinvitingAnAddressInAnotherCaseReplacesTheInvite(t *testing.T) {
	store, inviter := setupOrgTest(t)
	ctx := context.Background()
	const org = "org-email-fold"
	if err := store.CreateOrg(ctx, &domain.Org{ID: org, Name: "Fold", Slug: "fold", Tier: domain.Free, OwnerID: inviter}); err != nil {
		t.Fatalf("CreateOrg: %v", err)
	}
	expires := &domain.FlexTime{Time: time.Now().Add(time.Hour)}
	for i, address := range []string{"Dev@X.example.com", "dev@x.example.com"} {
		invite := &domain.PendingInvite{OrgID: org, Email: address, Role: domain.OrgRoleDeveloper,
			InvitedBy: inviter, TokenHash: "hash-" + string(rune('a'+i)), ExpiresAt: expires}
		if err := store.CreatePendingInvite(ctx, invite); err != nil {
			t.Fatalf("invite %s: %v", address, err)
		}
	}
	invites, err := store.ListPendingInvites(ctx, org)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(invites) != 1 {
		t.Fatalf("two open invites for one address: %+v", invites)
	}
}
