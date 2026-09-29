//go:build integration

package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/domain"
)

func fixtureGrant(id, projectID, resource, role string, ops ...domain.Operation) *domain.TableGrant {
	if len(ops) == 0 {
		ops = []domain.Operation{domain.OpSelect}
	}
	return &domain.TableGrant{
		ID: id, ProjectID: projectID, Resource: resource,
		Operations: ops, Role: role, Enabled: true,
	}
}

func TestTableGrants_UpsertListGet(t *testing.T) {
	store := NewTableGrants(testStore(t))
	ctx := context.Background()

	in := fixtureGrant("g1", "proj-a", "public.orders", domain.GrantRoleUser,
		domain.OpSelect, domain.OpInsert)
	if err := store.UpsertGrant(ctx, in); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	got, err := store.GetGrant(ctx, "proj-a", "g1")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Resource != "public.orders" || got.Role != domain.GrantRoleUser || !got.Enabled {
		t.Fatalf("round-trip mismatch: %+v", got)
	}
	if len(got.Operations) != 2 {
		t.Fatalf("want 2 operations, got %v", got.Operations)
	}
	if got.CreatedAt == nil || got.UpdatedAt == nil {
		t.Fatal("timestamps not populated")
	}

	list, err := store.ListGrants(ctx, "proj-a")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("want 1 grant, got %d", len(list))
	}
}

func TestTableGrants_ListIsProjectScopedAndNeverNil(t *testing.T) {
	store := NewTableGrants(testStore(t))
	ctx := context.Background()

	if err := store.UpsertGrant(ctx, fixtureGrant("g1", "proj-a", "public.orders", domain.GrantRoleAnon)); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	other, err := store.ListGrants(ctx, "proj-b")
	if err != nil {
		t.Fatalf("list other project: %v", err)
	}
	if other == nil {
		t.Fatal("ListGrants returned nil; must be an empty slice")
	}
	if len(other) != 0 {
		t.Fatalf("grants leaked across projects: %+v", other)
	}
}

func TestTableGrants_GetMissingReturnsNotFound(t *testing.T) {
	store := NewTableGrants(testStore(t))
	if _, err := store.GetGrant(context.Background(), "proj-a", "nope"); !errors.Is(err, ErrGrantNotFound) {
		t.Fatalf("want ErrGrantNotFound, got %v", err)
	}
}

func TestTableGrants_UpsertCannotStealAnotherProjectsGrant(t *testing.T) {
	store := NewTableGrants(testStore(t))
	ctx := context.Background()

	if err := store.UpsertGrant(ctx, fixtureGrant("g1", "proj-a", "public.orders", domain.GrantRoleAnon)); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	err := store.UpsertGrant(ctx, fixtureGrant("g1", "proj-evil", "public.secrets", domain.GrantRoleAnon))
	if err == nil {
		t.Fatal("expected cross-project upsert to be rejected")
	}

	kept, err := store.GetGrant(ctx, "proj-a", "g1")
	if err != nil {
		t.Fatalf("get after hijack attempt: %v", err)
	}
	if kept.Resource != "public.orders" {
		t.Fatalf("grant was overwritten by another project: %+v", kept)
	}
}

func TestTableGrants_Delete(t *testing.T) {
	store := NewTableGrants(testStore(t))
	ctx := context.Background()

	if err := store.UpsertGrant(ctx, fixtureGrant("g1", "proj-a", "public.orders", domain.GrantRoleAnon)); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if err := store.DeleteGrant(ctx, "proj-a", "g1"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if err := store.DeleteGrant(ctx, "proj-a", "g1"); !errors.Is(err, ErrGrantNotFound) {
		t.Fatalf("second delete: want ErrGrantNotFound, got %v", err)
	}
}

// EXC-400: the per-project opt-in table is gone. Its absence is the point —
// while it existed, a project with no row was served unfiltered, so granting
// one table changed nothing. Nothing may recreate it and no store method may
// read it back.
func TestTableGrants_PerProjectExposureSettingIsGone(t *testing.T) {
	store := NewTableGrants(testStore(t))

	var exists bool
	err := store.s.db.QueryRowContext(context.Background(),
		`SELECT EXISTS (SELECT 1 FROM information_schema.tables
		                WHERE table_name = 'project_exposure_settings')`).Scan(&exists)
	if err != nil {
		t.Fatalf("probe for the dropped table: %v", err)
	}
	if exists {
		t.Fatal("project_exposure_settings still exists; the per-project exposure hole is still open")
	}
}

// The schema itself refuses a role outside the end-user vocabulary, so a grant
// naming 'admin' or '*' cannot reach storage even past the API validator.
func TestTableGrants_StorageRefusesAMalformedOrServiceRole(t *testing.T) {
	store := NewTableGrants(testStore(t))
	ctx := context.Background()

	for i, role := range []string{"*", "service", "authenticated;x", "Admin", "9role", ""} {
		err := store.UpsertGrant(ctx, fixtureGrant(fmt.Sprintf("bad-%d", i), "proj-a", "public.orders", role))
		if err == nil {
			t.Errorf("role %q was stored; the schema must refuse it", role)
		}
	}
	for _, role := range []string{domain.GrantRoleAnon, domain.GrantRoleUser, "editor"} {
		if err := store.UpsertGrant(ctx, fixtureGrant("ok-"+role, "proj-a", "public.orders", role)); err != nil {
			t.Errorf("role %q must be storable: %v", role, err)
		}
	}
}

const (
	migrationBeforeGrantRoles = 63
	migrationGrantRoles       = 64
)

func grantRole(t *testing.T, store *Store, id string) (string, bool) {
	t.Helper()
	var role string
	err := store.DB().QueryRow(`SELECT role_name FROM table_grants WHERE id = $1`, id).Scan(&role)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false
	}
	if err != nil {
		t.Fatalf("read grant %s: %v", id, err)
	}
	return role, true
}

// authenticated was the signed-in end user; the engine now runs that caller as user.
func TestTableGrants_MigrationRenamesAuthenticatedToUserAndBack(t *testing.T) {
	store := testStore(t)
	if err := store.m.Migrate(migrationBeforeGrantRoles); err != nil {
		t.Fatalf("migrate down to %d: %v", migrationBeforeGrantRoles, err)
	}
	if _, err := store.DB().Exec(`INSERT INTO table_grants (id, project_id, resource, operations, role_name)
		VALUES ('g-anon', 'p', 'public.orders', '{SELECT}', 'anon'),
		       ('g-auth', 'p', 'public.orders', '{SELECT}', 'authenticated')`); err != nil {
		t.Fatalf("seed old-vocabulary grants: %v", err)
	}

	if err := store.m.Migrate(migrationGrantRoles); err != nil {
		t.Fatalf("migrate up to %d: %v", migrationGrantRoles, err)
	}
	if role, _ := grantRole(t, store, "g-auth"); role != domain.GrantRoleUser {
		t.Errorf("authenticated grant migrated to %q, want %q", role, domain.GrantRoleUser)
	}
	if role, _ := grantRole(t, store, "g-anon"); role != domain.GrantRoleAnon {
		t.Errorf("anon grant migrated to %q, want anon", role)
	}
	if _, err := store.DB().Exec(`INSERT INTO table_grants (id, project_id, resource, operations, role_name)
		VALUES ('g-custom', 'p', 'public.orders', '{SELECT}', 'editor')`); err != nil {
		t.Fatalf("a custom role must be storable after the migration: %v", err)
	}

	if err := store.m.Migrate(migrationBeforeGrantRoles); err != nil {
		t.Fatalf("migrate back down to %d: %v", migrationBeforeGrantRoles, err)
	}
	if role, _ := grantRole(t, store, "g-auth"); role != "authenticated" {
		t.Errorf("down migration left the user grant as %q, want authenticated", role)
	}
	if _, found := grantRole(t, store, "g-custom"); found {
		t.Error("down migration must drop grants the old vocabulary cannot express")
	}
}
