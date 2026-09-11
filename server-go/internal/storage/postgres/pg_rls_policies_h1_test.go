//go:build integration

package postgres

import (
	"context"
	"errors"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/domain"
)

// TestRlsPolicies_UpsertCannotHijackOtherProject pins SEC-H1: the upsert keys
// on id with ON CONFLICT DO UPDATE. Without a project ownership guard, one
// project could overwrite another project's policy by reusing its id
// (cross-tenant tampering / IDOR). The upsert must refuse to touch a row owned
// by a different project.
func TestRlsPolicies_UpsertCannotHijackOtherProject(t *testing.T) {
	store := NewRlsPolicies(testStore(t))
	ctx := context.Background()

	orig := fixtureRlsPolicy("shared-id", "proj-b", "orders")
	if err := store.UpsertRls(ctx, orig); err != nil {
		t.Fatalf("seed proj-b policy: %v", err)
	}

	attack := fixtureRlsPolicy("shared-id", "proj-a", "secrets")
	attack.Name = "hijacked"
	if err := store.UpsertRls(ctx, attack); err == nil {
		t.Errorf("upsert reusing another project's policy id must error (SEC-H1)")
	}

	got, err := store.GetRls(ctx, "proj-b", "shared-id")
	if err != nil {
		t.Fatalf("proj-b policy vanished: %v", err)
	}
	if got.Name != orig.Name || got.Resource != "orders" {
		t.Errorf("proj-b policy tampered: name=%q resource=%q", got.Name, got.Resource)
	}
	if _, err := store.GetRls(ctx, "proj-a", "shared-id"); !errors.Is(err, ErrPolicyNotFound) {
		t.Errorf("proj-a must not acquire the foreign policy; got err=%v", err)
	}
}

// TestColumnPolicies_UpsertCannotHijackOtherProject is the CLS twin of the
// above (column_policies has the same ON CONFLICT (id) shape).
func TestColumnPolicies_UpsertCannotHijackOtherProject(t *testing.T) {
	store := NewRlsPolicies(testStore(t))
	ctx := context.Background()

	orig := fixtureColumnPolicy("shared-col", "proj-b", "orders", domain.MaskHide)
	if err := store.UpsertColumn(ctx, orig); err != nil {
		t.Fatalf("seed proj-b column policy: %v", err)
	}

	attack := fixtureColumnPolicy("shared-col", "proj-a", "secrets", domain.MaskHide)
	attack.Name = "hijacked"
	if err := store.UpsertColumn(ctx, attack); err == nil {
		t.Errorf("column upsert reusing another project's id must error (SEC-H1)")
	}

	got, err := store.GetColumn(ctx, "proj-b", "shared-col")
	if err != nil {
		t.Fatalf("proj-b column policy vanished: %v", err)
	}
	if got.Name != orig.Name || got.Resource != "orders" {
		t.Errorf("proj-b column policy tampered: name=%q resource=%q", got.Name, got.Resource)
	}
}
