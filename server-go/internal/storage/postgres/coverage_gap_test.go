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

// PgDog config table CRUD — used by the PgDog notifier to register CNPG
// clusters. The tables come from migration 000019 so the same DDL PgDog
// reads in production is what these tests exercise.

func TestPostgres_PgDogDatabase_UpsertKeepsOneRowPerRoute(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()

	pool := 10
	db := &domain.PgDogDatabase{
		Name: "proj-x", Host: "h.svc.local", Port: 5432,
		DatabaseName: "appdb", Role: "primary", Shard: 0, PoolSize: &pool,
	}
	if err := store.RegisterPgDogDatabase(ctx, db); err != nil {
		t.Fatalf("RegisterPgDogDatabase: %v", err)
	}
	// Re-register with a moved host: still one row per (name, role, shard),
	// and the row must carry the new host so PgDog reconnects to the right place.
	db.Host = "h2.svc.local"
	if err := store.RegisterPgDogDatabase(ctx, db); err != nil {
		t.Fatalf("re-register: %v", err)
	}
	var rows int
	var host string
	if err := store.DB().QueryRowContext(ctx,
		`SELECT COUNT(*), MAX(host) FROM pgdog_databases WHERE name = 'proj-x' AND role = 'primary'`).
		Scan(&rows, &host); err != nil {
		t.Fatalf("count: %v", err)
	}
	if rows != 1 || host != "h2.svc.local" {
		t.Errorf("want 1 row with host h2.svc.local, got rows=%d host=%q", rows, host)
	}
	if err := store.RemovePgDogDatabase(ctx, "proj-x"); err != nil {
		t.Fatalf("RemovePgDogDatabase: %v", err)
	}
}

func TestPostgres_PgDogUsers_RegisterAndRemoveByDatabase(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()

	store.RegisterPgDogDatabase(ctx, &domain.PgDogDatabase{
		Name: testProjY, Host: "h", Port: 5432, DatabaseName: "appdb", Role: "primary",
	})

	pool := 5
	for _, name := range []string{"excalibase_app", "auth_admin"} {
		user := &domain.PgDogUser{Name: name, Database: testProjY, Password: testutil.FixtureSecret("pgdog-user"), PoolSize: &pool}
		if err := store.RegisterPgDogUser(ctx, user); err != nil {
			t.Fatalf("RegisterPgDogUser %s: %v", name, err)
		}
	}
	// Password rotation via ON CONFLICT (name, database).
	rotated := &domain.PgDogUser{Name: "excalibase_app", Database: testProjY, Password: testutil.FixtureSecret("pgdog-rotated")}
	if err := store.RegisterPgDogUser(ctx, rotated); err != nil {
		t.Fatalf("RegisterPgDogUser update: %v", err)
	}
	// A user of another project must survive the removal below.
	other := &domain.PgDogUser{Name: "excalibase_app", Database: "proj-other", Password: "o"}
	if err := store.RegisterPgDogUser(ctx, other); err != nil {
		t.Fatalf("RegisterPgDogUser other: %v", err)
	}

	if err := store.RemovePgDogUsers(ctx, testProjY); err != nil {
		t.Fatalf("RemovePgDogUsers: %v", err)
	}
	var left, otherLeft int
	store.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM pgdog_users WHERE database = $1`, testProjY).Scan(&left)
	store.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM pgdog_users WHERE database = 'proj-other'`).Scan(&otherLeft)
	if left != 0 {
		t.Errorf("expected every %s user removed, %d left", testProjY, left)
	}
	if otherLeft != 1 {
		t.Errorf("other project's user must be untouched, got %d", otherLeft)
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
