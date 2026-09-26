//go:build integration

package postgres

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/storage"
)

func TestOAuthStateIsReturnedOnceAndExpires(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	now := time.Now()
	state := domain.OAuthState{Provider: "google", CodeVerifier: "verifier", InviteHash: "invite"}
	if err := store.SaveOAuthState(ctx, "live", state, now.Add(time.Minute)); err != nil {
		t.Fatalf("save: %v", err)
	}
	if err := store.SaveOAuthState(ctx, "stale", state, now.Add(-time.Second)); err != nil {
		t.Fatalf("save: %v", err)
	}

	got, err := store.ConsumeOAuthState(ctx, "live", now)
	if err != nil || *got != state {
		t.Fatalf("consume: %+v %v", got, err)
	}
	if _, err := store.ConsumeOAuthState(ctx, "live", now); !errors.Is(err, storage.ErrOAuthStateInvalid) {
		t.Fatalf("second consume: %v", err)
	}
	if _, err := store.ConsumeOAuthState(ctx, "stale", now); !errors.Is(err, storage.ErrOAuthStateInvalid) {
		t.Fatalf("expired consume: %v", err)
	}
}

func TestStudioIdentityLinksToOneAccount(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	user := seedUnverifiedUser(t, store, "linked")

	if found, err := store.FindUserByIdentity(ctx, "github", "4242"); err != nil || found != nil {
		t.Fatalf("before linking: %+v %v", found, err)
	}
	if err := store.LinkStudioIdentity(ctx, "github", "4242", user.ID, user.Email); err != nil {
		t.Fatalf("link: %v", err)
	}
	found, err := store.FindUserByIdentity(ctx, "github", "4242")
	if err != nil || found == nil || found.ID != user.ID {
		t.Fatalf("after linking: %+v %v", found, err)
	}
	other := seedUnverifiedUser(t, store, "other")
	if err := store.LinkStudioIdentity(ctx, "github", "4242", other.ID, other.Email); err == nil {
		t.Fatal("one provider account was linked to a second Studio account")
	}
}

func TestFindUserByEmailFoldIgnoresCase(t *testing.T) {
	store := testStore(t)
	user := seedUnverifiedUser(t, store, "Mixed")
	found, err := store.FindUserByEmailFold(context.Background(), "mixed@VERIFY.example.com")
	if err != nil || found == nil || found.ID != user.ID {
		t.Fatalf("got %+v %v", found, err)
	}
}
