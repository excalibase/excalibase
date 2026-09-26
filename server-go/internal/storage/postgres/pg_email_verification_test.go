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

func seedUnverifiedUser(t *testing.T, store *Store, id string) *domain.User {
	t.Helper()
	user := &domain.User{
		ID: id, Username: "u-" + id, Email: id + "@verify.example.com",
		PasswordHash: testutil.FixturePasswordHash(), Role: "user", Active: true,
	}
	if err := store.CreateUser(context.Background(), user); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	return user
}

func verifiedAt(t *testing.T, store *Store, id string) *time.Time {
	t.Helper()
	user, err := store.FindUserByID(context.Background(), id)
	if err != nil || user == nil {
		t.Fatalf("FindUserByID: %v", err)
	}
	return user.EmailVerifiedAt
}

func TestNewUserStartsUnverified(t *testing.T) {
	store := testStore(t)
	seedUnverifiedUser(t, store, "fresh")
	if got := verifiedAt(t, store, "fresh"); got != nil {
		t.Fatalf("a new account is verified at %v", got)
	}
}

func TestCreateUserKeepsAGivenVerification(t *testing.T) {
	store := testStore(t)
	now := time.Now().UTC().Truncate(time.Second)
	user := &domain.User{ID: "pre", Username: "pre", Email: "pre@verify.example.com",
		PasswordHash: testutil.FixturePasswordHash(), Role: "user", Active: true, EmailVerifiedAt: &now}
	if err := store.CreateUser(context.Background(), user); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	if got := verifiedAt(t, store, "pre"); got == nil || !got.Equal(now) {
		t.Fatalf("verified at %v, want %v", got, now)
	}
}

func TestConsumingAVerificationMarksTheUserOnce(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	user := seedUnverifiedUser(t, store, "consume")
	now := time.Now().UTC()
	if err := store.CreateEmailVerification(ctx, user.ID, user.Email, "hash-consume", now.Add(time.Hour)); err != nil {
		t.Fatalf("CreateEmailVerification: %v", err)
	}

	userID, err := store.ConsumeEmailVerification(ctx, "hash-consume", now)
	if err != nil || userID != user.ID {
		t.Fatalf("consume: user %q err %v", userID, err)
	}
	if verifiedAt(t, store, user.ID) == nil {
		t.Fatal("user not verified after consuming the link")
	}
	if _, err := store.ConsumeEmailVerification(ctx, "hash-consume", now); !errors.Is(err, storage.ErrEmailVerificationInvalid) {
		t.Fatalf("second consume: got %v, want ErrEmailVerificationInvalid", err)
	}
}

func TestAnExpiredVerificationVerifiesNothing(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	user := seedUnverifiedUser(t, store, "expired")
	now := time.Now().UTC()
	if err := store.CreateEmailVerification(ctx, user.ID, user.Email, "hash-expired", now.Add(-time.Minute)); err != nil {
		t.Fatalf("CreateEmailVerification: %v", err)
	}
	if _, err := store.ConsumeEmailVerification(ctx, "hash-expired", now); !errors.Is(err, storage.ErrEmailVerificationInvalid) {
		t.Fatalf("got %v, want ErrEmailVerificationInvalid", err)
	}
	if verifiedAt(t, store, user.ID) != nil {
		t.Fatal("an expired link verified the account")
	}
}

// A link proves the mailbox it was sent to, so it verifies nothing once the
// account carries a different address.
func TestAVerificationForAnotherAddressVerifiesNothing(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	user := seedUnverifiedUser(t, store, "moved")
	now := time.Now().UTC()
	if err := store.CreateEmailVerification(ctx, user.ID, "old@verify.example.com", "hash-moved", now.Add(time.Hour)); err != nil {
		t.Fatalf("CreateEmailVerification: %v", err)
	}
	if _, err := store.ConsumeEmailVerification(ctx, "hash-moved", now); !errors.Is(err, storage.ErrEmailVerificationInvalid) {
		t.Fatalf("got %v, want ErrEmailVerificationInvalid", err)
	}
	if verifiedAt(t, store, user.ID) != nil {
		t.Fatal("a link for another address verified the account")
	}
}

func TestMarkEmailVerified(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	user := seedUnverifiedUser(t, store, "marked")
	if err := store.MarkEmailVerified(ctx, user.ID, time.Now()); err != nil {
		t.Fatalf("MarkEmailVerified: %v", err)
	}
	if verifiedAt(t, store, user.ID) == nil {
		t.Fatal("not verified")
	}
	if err := store.MarkEmailVerified(ctx, "nobody", time.Now()); !errors.Is(err, storage.ErrUserNotFound) {
		t.Fatalf("unknown user: got %v, want ErrUserNotFound", err)
	}
}

func TestFirstAdminIsCreatedWithItsVerification(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	if err := store.StoreSetupTokenHash(ctx, "hash-first"); err != nil {
		t.Fatalf("StoreSetupTokenHash: %v", err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	admin := newSetupTokenAdmin("first")
	admin.EmailVerifiedAt = &now
	if err := store.CreateFirstAdmin(ctx, "hash-first", admin); err != nil {
		t.Fatalf("CreateFirstAdmin: %v", err)
	}
	if got := verifiedAt(t, store, "first"); got == nil || !got.Equal(now) {
		t.Fatalf("first admin verified at %v, want %v", got, now)
	}
}
