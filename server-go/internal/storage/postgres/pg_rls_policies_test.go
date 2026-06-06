//go:build integration

package postgres

import (
	"context"
	"errors"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/domain"
)

// fixtureRlsPolicy returns a minimal-but-valid Policy that round-trips through
// upsert + scan. Tests mutate the returned pointer freely.
func fixtureRlsPolicy(id, projectID, resource string) *domain.Policy {
	return &domain.Policy{
		ID: id, ProjectID: projectID, Name: "p-" + id, Resource: resource,
		Effect:     domain.EffectAllow,
		Operations: []domain.Operation{domain.OpSelect},
		RuleLogic:  domain.LogicAnd,
		Rules: []domain.Rule{
			{Field: "user_id", FieldType: domain.FieldString, Operator: domain.OpEQ, Value: "ctx.user_id"},
		},
		Assignments: []domain.Assignment{
			{TargetType: domain.TargetUser, TargetID: "*"},
		},
		Priority: 100,
		Enabled:  true,
	}
}

func fixtureColumnPolicy(id, projectID, resource string, mode domain.MaskMode) *domain.ColumnPolicy {
	return &domain.ColumnPolicy{
		ID: id, ProjectID: projectID, Name: "c-" + id, Resource: resource,
		Columns:    []string{"email", "phone"},
		Operations: []domain.Operation{domain.OpSelect},
		Mode:       mode,
		Assignments: []domain.Assignment{
			{TargetType: domain.TargetRole, TargetID: "viewer"},
		},
		Priority: 50,
		Enabled:  true,
	}
}

func TestRlsPolicies_UpsertAndGet(t *testing.T) {
	store := NewRlsPolicies(testStore(t))
	ctx := context.Background()

	in := fixtureRlsPolicy("r1", "proj-a", "orders")
	if err := store.UpsertRls(ctx, in); err != nil {
		t.Fatalf("UpsertRls: %v", err)
	}
	got, err := store.GetRls(ctx, "proj-a", "r1")
	if err != nil {
		t.Fatalf("GetRls: %v", err)
	}
	if got.Name != in.Name || got.Resource != "orders" || got.Effect != domain.EffectAllow {
		t.Errorf("scalar fields wrong: %+v", got)
	}
	if len(got.Operations) != 1 || got.Operations[0] != domain.OpSelect {
		t.Errorf("operations: %v", got.Operations)
	}
	if len(got.Rules) != 1 || got.Rules[0].Field != "user_id" {
		t.Errorf("rules round-trip: %+v", got.Rules)
	}
	if len(got.Assignments) != 1 || got.Assignments[0].TargetID != "*" {
		t.Errorf("assignments round-trip: %+v", got.Assignments)
	}
	if got.Priority != 100 || !got.Enabled {
		t.Errorf("priority/enabled: %d %v", got.Priority, got.Enabled)
	}
	if got.CreatedAt == nil || got.UpdatedAt == nil {
		t.Errorf("timestamps nil: %+v / %+v", got.CreatedAt, got.UpdatedAt)
	}
}

func TestRlsPolicies_GetNotFound(t *testing.T) {
	store := NewRlsPolicies(testStore(t))
	_, err := store.GetRls(context.Background(), "proj-a", "missing")
	if !errors.Is(err, ErrPolicyNotFound) {
		t.Errorf("want ErrPolicyNotFound, got %v", err)
	}
}

func TestRlsPolicies_ListScopedByProject(t *testing.T) {
	store := NewRlsPolicies(testStore(t))
	ctx := context.Background()

	store.UpsertRls(ctx, fixtureRlsPolicy("r1", "proj-a", "orders"))
	store.UpsertRls(ctx, fixtureRlsPolicy("r2", "proj-a", "users"))
	store.UpsertRls(ctx, fixtureRlsPolicy("r3", "proj-b", "orders"))

	a, _ := store.ListRls(ctx, "proj-a", "")
	if len(a) != 2 {
		t.Errorf("proj-a: want 2, got %d", len(a))
	}
	b, _ := store.ListRls(ctx, "proj-b", "")
	if len(b) != 1 || b[0].ID != "r3" {
		t.Errorf("proj-b: %+v", b)
	}
}

func TestRlsPolicies_ListFilteredByResource(t *testing.T) {
	store := NewRlsPolicies(testStore(t))
	ctx := context.Background()

	store.UpsertRls(ctx, fixtureRlsPolicy("r1", "proj-a", "orders"))
	store.UpsertRls(ctx, fixtureRlsPolicy("r2", "proj-a", "users"))

	got, _ := store.ListRls(ctx, "proj-a", "orders")
	if len(got) != 1 || got[0].Resource != "orders" {
		t.Errorf("filter by resource: %+v", got)
	}
}

func TestRlsPolicies_UpsertReplacesByID(t *testing.T) {
	store := NewRlsPolicies(testStore(t))
	ctx := context.Background()

	p := fixtureRlsPolicy("r1", "proj-a", "orders")
	store.UpsertRls(ctx, p)

	p.Name = "renamed"
	p.Priority = 999
	p.Effect = domain.EffectDeny
	store.UpsertRls(ctx, p)

	got, _ := store.GetRls(ctx, "proj-a", "r1")
	if got.Name != "renamed" || got.Priority != 999 || got.Effect != domain.EffectDeny {
		t.Errorf("upsert did not overwrite: %+v", got)
	}
}

func TestRlsPolicies_Delete(t *testing.T) {
	store := NewRlsPolicies(testStore(t))
	ctx := context.Background()

	store.UpsertRls(ctx, fixtureRlsPolicy("r1", "proj-a", "orders"))
	if err := store.DeleteRls(ctx, "proj-a", "r1"); err != nil {
		t.Fatalf("DeleteRls: %v", err)
	}
	_, err := store.GetRls(ctx, "proj-a", "r1")
	if !errors.Is(err, ErrPolicyNotFound) {
		t.Errorf("want ErrPolicyNotFound after delete, got %v", err)
	}
}

func TestRlsPolicies_DeleteRespectsProjectScope(t *testing.T) {
	store := NewRlsPolicies(testStore(t))
	ctx := context.Background()

	store.UpsertRls(ctx, fixtureRlsPolicy("r1", "proj-a", "orders"))
	// Wrong-project delete must not affect the row, and must report not-found.
	if err := store.DeleteRls(ctx, "proj-b", "r1"); !errors.Is(err, ErrPolicyNotFound) {
		t.Errorf("cross-project delete: want ErrPolicyNotFound, got %v", err)
	}
	if _, err := store.GetRls(ctx, "proj-a", "r1"); err != nil {
		t.Errorf("row should still exist for proj-a: %v", err)
	}
}

// -------------------- Column policy tests --------------------

func TestColumnPolicies_RoundTrip_HideMode(t *testing.T) {
	store := NewRlsPolicies(testStore(t))
	ctx := context.Background()

	in := fixtureColumnPolicy("c1", "proj-a", "users", domain.MaskHide)
	if err := store.UpsertColumn(ctx, in); err != nil {
		t.Fatalf("UpsertColumn: %v", err)
	}
	got, err := store.GetColumn(ctx, "proj-a", "c1")
	if err != nil {
		t.Fatalf("GetColumn: %v", err)
	}
	if got.Mode != domain.MaskHide || len(got.Columns) != 2 {
		t.Errorf("hide round-trip: %+v", got)
	}
	if got.PartialSpec != nil || got.CustomMaskerKey != "" {
		t.Errorf("hide mode leaked partial/custom fields: %+v / %q", got.PartialSpec, got.CustomMaskerKey)
	}
}

func TestColumnPolicies_RoundTrip_PartialMode(t *testing.T) {
	store := NewRlsPolicies(testStore(t))
	ctx := context.Background()

	in := fixtureColumnPolicy("c1", "proj-a", "users", domain.MaskPartial)
	in.PartialSpec = &domain.PartialMaskSpec{
		Kind: "KEEP_FIRST", N: 4, MaskChar: "*",
	}
	if err := store.UpsertColumn(ctx, in); err != nil {
		t.Fatalf("UpsertColumn: %v", err)
	}
	got, _ := store.GetColumn(ctx, "proj-a", "c1")
	if got.PartialSpec == nil || got.PartialSpec.Kind != "KEEP_FIRST" || got.PartialSpec.N != 4 || got.PartialSpec.MaskChar != "*" {
		t.Errorf("partial spec round-trip: %+v", got.PartialSpec)
	}
}

func TestColumnPolicies_RoundTrip_CustomMode(t *testing.T) {
	store := NewRlsPolicies(testStore(t))
	ctx := context.Background()

	in := fixtureColumnPolicy("c1", "proj-a", "users", domain.MaskCustom)
	in.CustomMaskerKey = "ssn-masker"
	store.UpsertColumn(ctx, in)

	got, _ := store.GetColumn(ctx, "proj-a", "c1")
	if got.CustomMaskerKey != "ssn-masker" {
		t.Errorf("custom key round-trip: %q", got.CustomMaskerKey)
	}
}

func TestColumnPolicies_ListScopedByProject(t *testing.T) {
	store := NewRlsPolicies(testStore(t))
	ctx := context.Background()

	store.UpsertColumn(ctx, fixtureColumnPolicy("c1", "proj-a", "users", domain.MaskNull))
	store.UpsertColumn(ctx, fixtureColumnPolicy("c2", "proj-b", "users", domain.MaskNull))

	a, _ := store.ListColumn(ctx, "proj-a", "")
	if len(a) != 1 || a[0].ID != "c1" {
		t.Errorf("proj-a column policies: %+v", a)
	}
}

func TestColumnPolicies_DeleteRespectsProjectScope(t *testing.T) {
	store := NewRlsPolicies(testStore(t))
	ctx := context.Background()

	store.UpsertColumn(ctx, fixtureColumnPolicy("c1", "proj-a", "users", domain.MaskNull))
	if err := store.DeleteColumn(ctx, "proj-b", "c1"); !errors.Is(err, ErrPolicyNotFound) {
		t.Errorf("cross-project column delete: want ErrPolicyNotFound, got %v", err)
	}
	if _, err := store.GetColumn(ctx, "proj-a", "c1"); err != nil {
		t.Errorf("row should still exist: %v", err)
	}
}
