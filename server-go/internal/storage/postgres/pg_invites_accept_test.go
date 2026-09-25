//go:build integration

package postgres

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/storage"
	"github.com/excalibase/provisioning-poc/internal/testutil"
)

func seedAcceptableInvite(t *testing.T, store *Store, inviter, hash string, expires time.Time) {
	t.Helper()
	if err := store.CreatePendingInvite(context.Background(), &domain.PendingInvite{
		OrgID: testOrgInv, Email: testNewGuyEmail, Role: "developer", InvitedBy: inviter,
		TokenHash: hash, ExpiresAt: &domain.FlexTime{Time: expires},
	}); err != nil {
		t.Fatalf("CreatePendingInvite: %v", err)
	}
}

func addInvitee(t *testing.T, store *Store, id string) {
	t.Helper()
	if err := store.CreateUser(context.Background(), &domain.User{
		ID: id, Username: testutil.FixtureToken(id), Email: id + "@test.com",
		PasswordHash: testutil.FixturePasswordHash(), Role: "user", Active: true,
	}); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
}

func TestAcceptPendingInviteJoinsOnce(t *testing.T) {
	store, inviter := setupOrgTest(t)
	ctx := context.Background()
	store.CreateOrg(ctx, &domain.Org{ID: testOrgInv, Name: "Invites", Slug: "invites", Tier: domain.Free, OwnerID: inviter})
	addInvitee(t, store, "invitee-1")
	addInvitee(t, store, "invitee-2")
	seedAcceptableInvite(t, store, inviter, "hash-a", time.Now().Add(time.Hour))

	inv, err := store.AcceptPendingInvite(ctx, "hash-a", "invitee-1", time.Now())
	if err != nil {
		t.Fatalf("AcceptPendingInvite: %v", err)
	}
	if inv.OrgID != testOrgInv || inv.Role != "developer" {
		t.Errorf("accepted %+v", inv)
	}
	member, err := store.GetOrgMember(ctx, testOrgInv, "invitee-1")
	if err != nil || member == nil || member.Role != "developer" {
		t.Fatalf("membership = %+v, %v", member, err)
	}
	if _, err := store.AcceptPendingInvite(ctx, "hash-a", "invitee-2", time.Now()); !errors.Is(err, storage.ErrInviteInvalid) {
		t.Errorf("a spent invite must be invalid, got %v", err)
	}
}

func TestAcceptPendingInviteRefusesAnExpiredInvite(t *testing.T) {
	store, inviter := setupOrgTest(t)
	ctx := context.Background()
	store.CreateOrg(ctx, &domain.Org{ID: testOrgInv, Name: "Invites", Slug: "invites", Tier: domain.Free, OwnerID: inviter})
	addInvitee(t, store, "invitee-1")
	seedAcceptableInvite(t, store, inviter, "hash-old", time.Now().Add(-time.Minute))

	if _, err := store.AcceptPendingInvite(ctx, "hash-old", "invitee-1", time.Now()); !errors.Is(err, storage.ErrInviteInvalid) {
		t.Fatalf("an expired invite must be invalid, got %v", err)
	}
	if m, _ := store.GetOrgMember(ctx, testOrgInv, "invitee-1"); m != nil {
		t.Error("an expired invite added a member")
	}
}

func TestAcceptPendingInviteLeavesTheInviteForAnExistingMember(t *testing.T) {
	store, inviter := setupOrgTest(t)
	ctx := context.Background()
	store.CreateOrg(ctx, &domain.Org{ID: testOrgInv, Name: "Invites", Slug: "invites", Tier: domain.Free, OwnerID: inviter})
	store.AddOrgMember(ctx, &domain.OrgMember{OrgID: testOrgInv, UserID: inviter, Role: "owner"})
	seedAcceptableInvite(t, store, inviter, "hash-m", time.Now().Add(time.Hour))

	if _, err := store.AcceptPendingInvite(ctx, "hash-m", inviter, time.Now()); !errors.Is(err, storage.ErrAlreadyOrgMember) {
		t.Fatalf("got %v, want ErrAlreadyOrgMember", err)
	}
	if _, err := store.FindPendingInviteByToken(ctx, "hash-m", time.Now()); err != nil {
		t.Errorf("the invite must stay unspent: %v", err)
	}
	if m, _ := store.GetOrgMember(ctx, testOrgInv, inviter); m == nil || m.Role != "owner" {
		t.Errorf("the existing membership changed: %+v", m)
	}
}

func TestAcceptPendingInviteFailsOnAClosedStore(t *testing.T) {
	store, _ := setupOrgTest(t)
	store.DB().Close()
	if _, err := store.AcceptPendingInvite(context.Background(), "h", "u", time.Now()); err == nil || errors.Is(err, storage.ErrInviteInvalid) {
		t.Fatalf("a store that cannot answer must fail, not read as an invalid invite: %v", err)
	}
}

func TestPendingInviteTokenIsRequired(t *testing.T) {
	store, _ := setupOrgTest(t)
	var nullable string
	if err := store.DB().QueryRow(`SELECT is_nullable FROM information_schema.columns
		WHERE table_name = 'pending_invites' AND column_name = 'token_hash'`).Scan(&nullable); err != nil {
		t.Fatalf("read column: %v", err)
	}
	if nullable != "NO" {
		t.Errorf("token_hash must be required, is_nullable=%s", nullable)
	}
}
