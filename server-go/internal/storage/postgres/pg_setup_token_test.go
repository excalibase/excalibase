//go:build integration

package postgres

import (
	"context"
	"errors"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/storage"
	"github.com/excalibase/provisioning-poc/internal/testutil"
)

func newSetupTokenAdmin(id string) *domain.User {
	return &domain.User{
		ID:           id,
		Username:     "founder-" + id,
		Email:        id + "@setup-token-test.example.com",
		PasswordHash: testutil.FixturePasswordHash(),
		Role:         "platform_admin",
		Active:       true,
		Kind:         domain.UserKindHuman,
	}
}

// A fresh store has no platform admin; creating one through CreateFirstAdmin
// flips HasPlatformAdmin to true.
func TestHasPlatformAdmin_FalseThenTrueAfterFirstAdmin(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()

	has, err := store.HasPlatformAdmin(ctx)
	if err != nil {
		t.Fatalf("HasPlatformAdmin: %v", err)
	}
	if has {
		t.Fatal("expected no platform admin on a fresh store")
	}

	if err := store.StoreSetupTokenHash(ctx, "hash-a"); err != nil {
		t.Fatalf("StoreSetupTokenHash: %v", err)
	}
	if err := store.CreateFirstAdmin(ctx, "hash-a", newSetupTokenAdmin("admin-1")); err != nil {
		t.Fatalf("CreateFirstAdmin: %v", err)
	}

	has, err = store.HasPlatformAdmin(ctx)
	if err != nil {
		t.Fatalf("HasPlatformAdmin (after): %v", err)
	}
	if !has {
		t.Fatal("expected a platform admin after CreateFirstAdmin")
	}
}

// StoreSetupTokenHash replaces whatever hash was stored before — only the
// most recently stored hash may ever be burned.
func TestStoreSetupTokenHash_ReplacesPreviousToken(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()

	if err := store.StoreSetupTokenHash(ctx, "hash-old"); err != nil {
		t.Fatalf("store old hash: %v", err)
	}
	if err := store.StoreSetupTokenHash(ctx, "hash-new"); err != nil {
		t.Fatalf("store new hash: %v", err)
	}

	if err := store.CreateFirstAdmin(ctx, "hash-old", newSetupTokenAdmin("admin-old")); !errors.Is(err, storage.ErrInvalidSetupToken) {
		t.Fatalf("burning the replaced hash: got %v, want ErrInvalidSetupToken", err)
	}
	if err := store.CreateFirstAdmin(ctx, "hash-new", newSetupTokenAdmin("admin-new")); err != nil {
		t.Fatalf("burning the current hash: %v", err)
	}
}

// A tokenHash that was never stored (or already burned) is refused, and
// refusing it must not create a user.
func TestCreateFirstAdmin_WrongHash_NoUserCreated(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()

	if err := store.StoreSetupTokenHash(ctx, "hash-real"); err != nil {
		t.Fatalf("StoreSetupTokenHash: %v", err)
	}
	if err := store.CreateFirstAdmin(ctx, "hash-wrong", newSetupTokenAdmin("admin-wrong")); !errors.Is(err, storage.ErrInvalidSetupToken) {
		t.Fatalf("got %v, want ErrInvalidSetupToken", err)
	}

	users, err := store.FindAllUsers(ctx)
	if err != nil {
		t.Fatalf("FindAllUsers: %v", err)
	}
	if len(users) != 0 {
		t.Fatalf("a refused setup token created %d user(s)", len(users))
	}
}

// The token is burned exactly once: a second CreateFirstAdmin with the same
// hash — even one that just succeeded — must fail.
func TestCreateFirstAdmin_TokenBurnedOnFirstUse(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()

	if err := store.StoreSetupTokenHash(ctx, "hash-once"); err != nil {
		t.Fatalf("StoreSetupTokenHash: %v", err)
	}
	if err := store.CreateFirstAdmin(ctx, "hash-once", newSetupTokenAdmin("admin-once")); err != nil {
		t.Fatalf("first CreateFirstAdmin: %v", err)
	}
	if err := store.CreateFirstAdmin(ctx, "hash-once", newSetupTokenAdmin("admin-twice")); !errors.Is(err, storage.ErrInvalidSetupToken) {
		t.Fatalf("reusing a burned token: got %v, want ErrInvalidSetupToken", err)
	}
}

// A dead connection surfaces as a wrapped error, not a panic or a silent
// false, on every one of the three setup-token calls.
func TestSetupToken_ConnectionClosed_WrappedErrors(t *testing.T) {
	store := testStore(t)
	store.DB().Close()

	if _, err := store.HasPlatformAdmin(context.Background()); err == nil {
		t.Error("HasPlatformAdmin over a closed connection must error")
	}
	if err := store.StoreSetupTokenHash(context.Background(), "hash"); err == nil {
		t.Error("StoreSetupTokenHash over a closed connection must error")
	}
	if err := store.CreateFirstAdmin(context.Background(), "hash", newSetupTokenAdmin("admin-dead")); err == nil {
		t.Error("CreateFirstAdmin over a closed connection must error")
	}
}

// The DELETE inside StoreSetupTokenHash's transaction is itself the only
// statement that can fail once BeginTx succeeds; dropping the table forces
// that specific failure without touching the connection.
func TestStoreSetupTokenHash_DeleteFails_WrappedError(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()

	if _, err := store.DB().Exec(`DROP TABLE setup_tokens`); err != nil {
		t.Fatalf("drop setup_tokens: %v", err)
	}
	if err := store.StoreSetupTokenHash(ctx, "hash"); err == nil {
		t.Fatal("expected an error once the setup_tokens table is gone")
	}
}

// Same failure, reached through CreateFirstAdmin's own DELETE (the burn).
func TestCreateFirstAdmin_DeleteFails_WrappedError(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()

	if err := store.StoreSetupTokenHash(ctx, "hash-before-drop"); err != nil {
		t.Fatalf("StoreSetupTokenHash: %v", err)
	}
	if _, err := store.DB().Exec(`DROP TABLE setup_tokens`); err != nil {
		t.Fatalf("drop setup_tokens: %v", err)
	}
	if err := store.CreateFirstAdmin(ctx, "hash-before-drop", newSetupTokenAdmin("admin-drop")); err == nil {
		t.Fatal("expected an error once the setup_tokens table is gone")
	}
}

// The final INSERT can fail on its own (a duplicate user id) even though the
// token burn (the DELETE just before it) succeeded. The whole transaction
// must then roll back together — the token must NOT come out burned, so a
// second, valid attempt can still use it.
func TestCreateFirstAdmin_InsertUserFails_TokenNotBurned(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()

	dup := newSetupTokenAdmin("dup-id")
	if err := store.CreateUser(ctx, dup); err != nil {
		t.Fatalf("seed colliding user: %v", err)
	}

	if err := store.StoreSetupTokenHash(ctx, "hash-collide"); err != nil {
		t.Fatalf("StoreSetupTokenHash: %v", err)
	}
	// Same ID as the row already in the table — the INSERT violates the
	// primary key, so this must fail without burning the token.
	if err := store.CreateFirstAdmin(ctx, "hash-collide", newSetupTokenAdmin("dup-id")); err == nil {
		t.Fatal("expected a primary-key conflict on the duplicate id")
	}

	// The transaction rolled back, so the token is still live.
	if err := store.CreateFirstAdmin(ctx, "hash-collide", newSetupTokenAdmin("admin-after-rollback")); err != nil {
		t.Fatalf("token should still be usable after the rollback: %v", err)
	}
}
