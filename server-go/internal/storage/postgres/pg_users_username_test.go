//go:build integration

package postgres

import (
	"context"
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/testutil"
)

const (
	migrationBeforeUsernameFold = 69
	migrationUsernameFold       = 70
)

func userNamed(id, username string) *domain.User {
	return &domain.User{
		ID: id, Username: username, Email: id + "@x.example.com",
		PasswordHash: testutil.FixturePasswordHash(), Role: "user", Active: true,
	}
}

func TestFindUserByUsernameIgnoresCaseAndKeepsTheStoredCasing(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	if err := store.CreateUser(ctx, userNamed("alice", "Alice")); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	found, err := store.FindUserByUsername(ctx, "aLICE")
	if err != nil || found == nil || found.ID != "alice" {
		t.Fatalf("lookup with other case: %+v %v", found, err)
	}
	if found.Username != "Alice" {
		t.Errorf("stored username was rewritten to %q; the original is kept for display", found.Username)
	}
	if err := store.UpdateUserPassword(ctx, "alice", "new-hash"); err != nil {
		t.Errorf("password update by another case: %v", err)
	}
}

// Alice and alice would read as the same person to everyone else.
func TestAUsernameDifferingOnlyByCaseIsRefused(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	if err := store.CreateUser(ctx, userNamed("first", "Alice")); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	if err := store.CreateUser(ctx, userNamed("second", "alice")); err == nil {
		t.Fatal("a second account took a username differing only by case")
	}
}

func TestUsernameFoldMigrationRefusesExistingCaseDuplicates(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	if err := store.m.Migrate(migrationBeforeUsernameFold); err != nil {
		t.Fatalf("migrate down to %d: %v", migrationBeforeUsernameFold, err)
	}
	for _, u := range []*domain.User{userNamed("a", "Alice"), userNamed("b", "alice")} {
		if err := store.CreateUser(ctx, u); err != nil {
			t.Fatalf("seed %s: %v", u.ID, err)
		}
	}
	err := store.m.Migrate(migrationUsernameFold)
	if err == nil {
		t.Fatal("the migration indexed lower(username) over two accounts sharing a name")
	}
	if !strings.Contains(err.Error(), "differ only by case") || !strings.Contains(err.Error(), "alice") {
		t.Fatalf("the failure does not name the duplicate username: %v", err)
	}
}

func TestUsernameFoldMigrationDownAllowsCaseVariantsAgain(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	if err := store.m.Migrate(migrationBeforeUsernameFold); err != nil {
		t.Fatalf("migrate down to %d: %v", migrationBeforeUsernameFold, err)
	}
	if err := store.CreateUser(ctx, userNamed("a", "Alice")); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := store.CreateUser(ctx, userNamed("b", "alice")); err != nil {
		t.Fatalf("down migration left the case-folded index: %v", err)
	}
}
