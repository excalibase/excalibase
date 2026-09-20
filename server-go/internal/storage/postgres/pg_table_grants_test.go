//go:build integration

package postgres

import (
	"context"
	"errors"
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

	in := fixtureGrant("g1", "proj-a", "public.orders", "authenticated",
		domain.OpSelect, domain.OpInsert)
	if err := store.UpsertGrant(ctx, in); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	got, err := store.GetGrant(ctx, "proj-a", "g1")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Resource != "public.orders" || got.Role != "authenticated" || !got.Enabled {
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
func TestTableGrants_StorageRefusesARoleOutsideTheEndUserRoles(t *testing.T) {
	store := NewTableGrants(testStore(t))
	ctx := context.Background()

	for _, role := range []string{"*", "user", "admin", "service_role"} {
		err := store.UpsertGrant(ctx, fixtureGrant("g-"+role, "proj-a", "public.orders", role))
		if err == nil {
			t.Errorf("role %q was stored; the schema must refuse it", role)
		}
	}
	for _, role := range []string{domain.GrantRoleAnon, domain.GrantRoleAuthenticated} {
		if err := store.UpsertGrant(ctx, fixtureGrant("ok-"+role, "proj-a", "public.orders", role)); err != nil {
			t.Errorf("role %q must be storable: %v", role, err)
		}
	}
}
