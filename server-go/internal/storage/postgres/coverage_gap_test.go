//go:build integration

package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/testutil"
)

const testProjY = "proj-y"

// FindAllOrgs / UpdateOrg / DeleteOrg cover the platform-admin org
// management surface. These were 0% because the smoke suite only walks
// the per-user query path.

func TestPostgres_OrgCRUD(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()

	store.CreateUser(ctx, &domain.User{
		ID: "u-pg", Username: "u", Email: "u@e.com",
		PasswordHash: testutil.FixturePasswordHash(), Role: "user", Active: true,
	})

	if err := store.CreateOrg(ctx, &domain.Org{
		ID: "o-1", Name: "Initial", Slug: "init", Tier: domain.Free, OwnerID: "u-pg",
	}); err != nil {
		t.Fatalf("CreateOrg: %v", err)
	}
	if err := store.CreateOrg(ctx, &domain.Org{
		ID: "o-2", Name: "Second", Slug: "second", Tier: domain.Free, OwnerID: "u-pg",
	}); err != nil {
		t.Fatalf("CreateOrg: %v", err)
	}

	all, err := store.FindAllOrgs(ctx)
	if err != nil {
		t.Fatalf("FindAllOrgs: %v", err)
	}
	if len(all) != 2 {
		t.Errorf("FindAllOrgs: got %d, want 2", len(all))
	}

	// Update name + tier; UpdatedAt should bump.
	if err := store.UpdateOrg(ctx, &domain.Org{
		ID: "o-1", Name: "Renamed", Slug: "init", Tier: domain.Standard, OwnerID: "u-pg",
	}); err != nil {
		t.Fatalf("UpdateOrg: %v", err)
	}
	got, err := store.FindOrgByID(ctx, "o-1")
	if err != nil || got == nil {
		t.Fatalf("FindOrgByID after update: %v / %v", err, got)
	}
	if got.Name != "Renamed" {
		t.Errorf("name after update: got %q, want Renamed", got.Name)
	}
	if got.Tier != domain.Standard {
		t.Errorf("tier after update: got %q, want STANDARD", got.Tier)
	}

	if err := store.DeleteOrg(ctx, "o-1"); err != nil {
		t.Fatalf("DeleteOrg: %v", err)
	}
	left, _ := store.FindAllOrgs(ctx)
	if len(left) != 1 {
		t.Errorf("after delete: got %d, want 1", len(left))
	}
}

// DB() and toFlexTime() are tiny helpers — test in-place to flip them off 0%.

func TestPostgres_DBAccessor(t *testing.T) {
	store := testStore(t)
	if store.DB() == nil {
		t.Error("DB() should expose the underlying *sql.DB")
	}
}

func TestPostgres_ToFlexTime(t *testing.T) {
	now := time.Now().UTC()
	got := toFlexTime(&now)
	if got == nil || !got.Time.Equal(now) {
		t.Errorf("toFlexTime: got %v, want wrapping %v", got, now)
	}
	if toFlexTime(nil) != nil {
		t.Error("toFlexTime(nil) should be nil")
	}
}
