//go:build integration

package postgres

import (
	"context"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/testutil"
)

// Migration 000025 adds users.kind and access_tokens.permissions. These tests
// run against a real Postgres so the round trip exercises the TEXT[] mapping
// and the kind check constraint, not just the Go structs.

func TestServiceUserRoundTrip(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()

	service := &domain.User{
		ID: "svc-1", Username: testutil.FixtureToken("svc-auth"), Email: "svc-auth@svc.test",
		Role: "platform_admin", Active: true, Kind: domain.UserKindService,
	}
	if err := store.CreateUser(ctx, service); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	got, err := store.FindUserByID(ctx, "svc-1")
	if err != nil || got == nil {
		t.Fatalf("FindUserByID: %v (got %v)", err, got)
	}
	if !got.IsService() {
		t.Fatalf("kind = %q, want service", got.Kind)
	}
}

func TestUserKindDefaultsToHuman(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()

	// A caller that predates the column leaves Kind empty; the row must still
	// come back as a human, never as a service principal.
	if err := store.CreateUser(ctx, &domain.User{
		ID: "hum-1", Username: testutil.FixtureToken("legacy"), Email: "legacy@test", Role: "user", Active: true,
	}); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	got, _ := store.FindUserByID(ctx, "hum-1")
	if got == nil || got.Kind != domain.UserKindHuman {
		t.Fatalf("kind = %+v, want human", got)
	}
	if got.IsService() {
		t.Fatal("a kind-less row must not be a service principal")
	}
}

func TestTokenPermissionsRoundTrip(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()

	store.CreateUser(ctx, &domain.User{
		ID: "svc-2", Username: testutil.FixtureToken("svc-graphql"), Email: "svc-graphql@svc.test",
		Role: "platform_admin", Active: true, Kind: domain.UserKindService,
	})
	want := []string{"policies:read", "projects:info:read", "vault:read:pki/signing/*"}
	if err := store.CreateToken(ctx, &domain.AccessToken{
		TokenHash: "perm-hash-1", TokenPrefix: "permprefix01", UserID: "svc-2",
		Name: "svc-graphql", Permissions: want,
	}); err != nil {
		t.Fatalf("CreateToken: %v", err)
	}

	got, err := store.FindByTokenHash(ctx, "perm-hash-1")
	if err != nil || got == nil {
		t.Fatalf("FindByTokenHash: %v (got %v)", err, got)
	}
	if len(got.Permissions) != len(want) {
		t.Fatalf("permissions = %v, want %v", got.Permissions, want)
	}
	for i := range want {
		if got.Permissions[i] != want[i] {
			t.Fatalf("permissions = %v, want %v", got.Permissions, want)
		}
	}

	listed, err := store.ListTokensByUser(ctx, "svc-2")
	if err != nil || len(listed) != 1 {
		t.Fatalf("ListTokensByUser: %v (%d rows)", err, len(listed))
	}
	if len(listed[0].Permissions) != len(want) {
		t.Fatalf("listed permissions = %v", listed[0].Permissions)
	}
}

func TestTokenWithoutPermissionsStaysEmpty(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()

	store.CreateUser(ctx, &domain.User{
		ID: "hum-2", Username: testutil.FixtureToken("humantok"), Email: "h2@test", Role: "user", Active: true,
	})
	if err := store.CreateToken(ctx, &domain.AccessToken{
		TokenHash: "plain-hash-1", TokenPrefix: "plainprefix1", UserID: "hum-2", Name: "ci",
	}); err != nil {
		t.Fatalf("CreateToken: %v", err)
	}
	got, _ := store.FindByTokenHash(ctx, "plain-hash-1")
	if got == nil || len(got.Permissions) != 0 {
		t.Fatalf("permissions = %+v, want none", got)
	}
}

func TestUserKindRejectsAnUnknownValue(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()

	err := store.CreateUser(ctx, &domain.User{
		ID: "bad-1", Username: testutil.FixtureToken("badkind"), Email: "bad@test",
		Role: "user", Active: true, Kind: "robot",
	})
	if err == nil {
		t.Fatal("the kind check constraint did not reject an unknown value")
	}
}
