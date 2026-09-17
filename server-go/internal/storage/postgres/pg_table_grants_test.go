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

	if err := store.UpsertGrant(ctx, fixtureGrant("g1", "proj-a", "public.orders", "*")); err != nil {
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

	if err := store.UpsertGrant(ctx, fixtureGrant("g1", "proj-a", "public.orders", "*")); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	err := store.UpsertGrant(ctx, fixtureGrant("g1", "proj-evil", "public.secrets", "*"))
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

	if err := store.UpsertGrant(ctx, fixtureGrant("g1", "proj-a", "public.orders", "*")); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if err := store.DeleteGrant(ctx, "proj-a", "g1"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if err := store.DeleteGrant(ctx, "proj-a", "g1"); !errors.Is(err, ErrGrantNotFound) {
		t.Fatalf("second delete: want ErrGrantNotFound, got %v", err)
	}
}

// A project that never opted in must read as unenforced — this is what keeps
// tenants provisioned before EXC-370 working after the migration lands.
func TestTableGrants_EnforcementDefaultsOffForExistingProjects(t *testing.T) {
	store := NewTableGrants(testStore(t))
	enforced, err := store.IsExposureEnforced(context.Background(), "proj-never-touched")
	if err != nil {
		t.Fatalf("read enforcement: %v", err)
	}
	if enforced {
		t.Fatal("project with no exposure setting must read as NOT enforced")
	}
}

func TestTableGrants_SetExposureEnforcedRoundTrips(t *testing.T) {
	store := NewTableGrants(testStore(t))
	ctx := context.Background()

	if err := store.SetExposureEnforced(ctx, "proj-a", true); err != nil {
		t.Fatalf("enable: %v", err)
	}
	on, err := store.IsExposureEnforced(ctx, "proj-a")
	if err != nil || !on {
		t.Fatalf("want enforced=true, got %v err=%v", on, err)
	}

	if err := store.SetExposureEnforced(ctx, "proj-a", false); err != nil {
		t.Fatalf("disable: %v", err)
	}
	off, err := store.IsExposureEnforced(ctx, "proj-a")
	if err != nil || off {
		t.Fatalf("want enforced=false, got %v err=%v", off, err)
	}

	// Enforcement is per project: toggling one must not touch another.
	otherEnforced, err := store.IsExposureEnforced(ctx, "proj-b")
	if err != nil || otherEnforced {
		t.Fatalf("enforcement leaked to another project: %v err=%v", otherEnforced, err)
	}
}
