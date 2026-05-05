package sqlite

import (
	"context"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/testutil"
)

const (
	testCarolEmail = "carol@example.com"
)


// FindAllOrgs covers the platform-admin org listing path (used by
// /admin/orgs in the studio), which is otherwise unreached by the
// per-user FindOrgsByUser path that the smoke tests exercise.
func TestSQLite_FindAllOrgs(t *testing.T) {
	store, _ := setupOrgTest(t)
	ctx := context.Background()

	store.CreateOrg(ctx, &domain.Org{ID: "o1", Name: "A", Slug: "a-x", Tier: domain.Free, OwnerID: testUser1})
	store.CreateOrg(ctx, &domain.Org{ID: "o2", Name: "B", Slug: "b-x", Tier: domain.Free, OwnerID: testUser1})

	all, err := store.FindAllOrgs(ctx)
	if err != nil {
		t.Fatalf("FindAllOrgs: %v", err)
	}
	if len(all) < 2 {
		t.Errorf("expected at least 2 orgs, got %d", len(all))
	}
}

func TestSQLite_FindAllOrgs_Empty(t *testing.T) {
	store := testStore(t)
	all, err := store.FindAllOrgs(context.Background())
	if err != nil {
		t.Fatalf("FindAllOrgs: %v", err)
	}
	if len(all) != 0 {
		t.Errorf("empty store: got %d orgs, want 0", len(all))
	}
}

// Pending invites — exercised by the org-invite-by-email flow.

func TestSQLite_PendingInvites_FullCRUD(t *testing.T) {
	store, _ := setupOrgTest(t)
	ctx := context.Background()
	store.CreateOrg(ctx, &domain.Org{ID: "o1", Name: "A", Slug: "alpha", Tier: domain.Free, OwnerID: testUser1})

	inv := &domain.PendingInvite{
		OrgID: "o1", Email: testCarolEmail, Role: "developer", InvitedBy: testUser1,
	}
	if err := store.CreatePendingInvite(ctx, inv); err != nil {
		t.Fatalf("CreatePendingInvite: %v", err)
	}

	byEmail, err := store.FindPendingInvitesByEmail(ctx, testCarolEmail)
	if err != nil {
		t.Fatalf("FindPendingInvitesByEmail: %v", err)
	}
	if len(byEmail) != 1 {
		t.Fatalf("by email: got %d, want 1", len(byEmail))
	}
	if byEmail[0].Role != "developer" {
		t.Errorf("role: got %s, want developer", byEmail[0].Role)
	}

	listed, err := store.ListPendingInvites(ctx, "o1")
	if err != nil {
		t.Fatalf("ListPendingInvites: %v", err)
	}
	if len(listed) != 1 {
		t.Errorf("ListPendingInvites: got %d, want 1", len(listed))
	}

	if err := store.DeletePendingInvite(ctx, byEmail[0].ID); err != nil {
		t.Fatalf("DeletePendingInvite: %v", err)
	}

	after, _ := store.FindPendingInvitesByEmail(ctx, testCarolEmail)
	if len(after) != 0 {
		t.Errorf("after delete: got %d, want 0", len(after))
	}
}

func TestSQLite_FindPendingInvitesByEmail_None(t *testing.T) {
	store, _ := setupOrgTest(t)
	got, err := store.FindPendingInvitesByEmail(context.Background(), "nobody@example.com")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("nonexistent: got %d, want 0", len(got))
	}
}

func TestSQLite_ListPendingInvites_Empty(t *testing.T) {
	store, _ := setupOrgTest(t)
	ctx := context.Background()
	store.CreateOrg(ctx, &domain.Org{ID: "empty-org", Name: "X", Slug: "x-empty", Tier: domain.Free, OwnerID: testUser1})

	got, err := store.ListPendingInvites(ctx, "empty-org")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("empty org: got %d, want 0", len(got))
	}
}

// FindUserByEmail — used by /auth/login when authentication is by email.

func TestSQLite_FindUserByEmail(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	dianaUser := testutil.FixtureToken("diana")
	store.CreateUser(ctx, &domain.User{
		ID: "u-1", Username: dianaUser, Email: "diana@example.com",
		PasswordHash: testutil.FixturePasswordHash(), Role: "user", Active: true,
	})

	got, err := store.FindUserByEmail(ctx, "diana@example.com")
	if err != nil {
		t.Fatalf("FindUserByEmail: %v", err)
	}
	if got.ID != "u-1" {
		t.Errorf("id: got %s", got.ID)
	}
	if got.Username != dianaUser {
		t.Errorf("username: got %s", got.Username)
	}
}

func TestSQLite_FindUserByEmail_NotFound(t *testing.T) {
	store := testStore(t)
	got, err := store.FindUserByEmail(context.Background(), "missing@example.com")
	if err != nil {
		t.Errorf("missing user should return nil/nil, got err=%v", err)
	}
	if got != nil {
		t.Errorf("missing user should return nil, got %+v", got)
	}
}
