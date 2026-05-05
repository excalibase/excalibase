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

// pgdogTablesSQL mirrors the schema created by the PgDog operator in
// production. We don't ship a migration for these tables (PgDog owns
// them), so tests have to build them inline.
const pgdogTablesSQL = `
CREATE TABLE IF NOT EXISTS pgdog_databases (
    name TEXT NOT NULL,
    host TEXT NOT NULL,
    port INTEGER NOT NULL,
    database_name TEXT NOT NULL,
    role TEXT NOT NULL,
    shard INTEGER NOT NULL DEFAULT 0,
    pool_size INTEGER,
    read_only BOOLEAN NOT NULL DEFAULT FALSE,
    active BOOLEAN NOT NULL DEFAULT TRUE,
    PRIMARY KEY (name, role, shard)
);
CREATE TABLE IF NOT EXISTS pgdog_users (
    name TEXT NOT NULL,
    database TEXT NOT NULL,
    password TEXT,
    pool_size INTEGER,
    active BOOLEAN NOT NULL DEFAULT TRUE,
    PRIMARY KEY (name, database)
);`

// PgDog config table CRUD — used by the PgDog notifier to register CNPG
// clusters. Verify each op against the real Postgres schema.

func TestPostgres_PgDogDatabase_RegisterAndRemove(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	if _, err := store.DB().ExecContext(ctx, pgdogTablesSQL); err != nil {
		t.Fatalf("create pgdog tables: %v", err)
	}

	pool := 10
	db := &domain.PgDogDatabase{
		Name: "proj-x", Host: "h.svc.local", Port: 5432,
		DatabaseName: "appdb", Role: "primary", Shard: 0, PoolSize: &pool,
	}
	if err := store.RegisterPgDogDatabase(ctx, db); err != nil {
		t.Fatalf("RegisterPgDogDatabase: %v", err)
	}
	// Re-register is idempotent (ON CONFLICT DO NOTHING).
	if err := store.RegisterPgDogDatabase(ctx, db); err != nil {
		t.Errorf("re-register should be idempotent, got %v", err)
	}
	if err := store.RemovePgDogDatabase(ctx, "proj-x"); err != nil {
		t.Fatalf("RemovePgDogDatabase: %v", err)
	}
}

func TestPostgres_PgDogUser_RegisterAndRemove(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	if _, err := store.DB().ExecContext(ctx, pgdogTablesSQL); err != nil {
		t.Fatalf("create pgdog tables: %v", err)
	}

	// Register the parent database first to satisfy any FK (if defined).
	store.RegisterPgDogDatabase(ctx, &domain.PgDogDatabase{
		Name: testProjY, Host: "h", Port: 5432, DatabaseName: "appdb", Role: "primary",
	})

	pool := 5
	user := &domain.PgDogUser{Name: "appuser", Database: testProjY, Password: testutil.FixtureSecret("pgdog-user"), PoolSize: &pool}
	if err := store.RegisterPgDogUser(ctx, user); err != nil {
		t.Fatalf("RegisterPgDogUser: %v", err)
	}
	// Update via ON CONFLICT
	user.Password = testutil.FixtureSecret("pgdog-rotated")
	if err := store.RegisterPgDogUser(ctx, user); err != nil {
		t.Fatalf("RegisterPgDogUser update: %v", err)
	}
	if err := store.RemovePgDogUser(ctx, "appuser", testProjY); err != nil {
		t.Fatalf("RemovePgDogUser: %v", err)
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
