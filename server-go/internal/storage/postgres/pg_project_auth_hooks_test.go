//go:build integration

package postgres

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
)

// Exercises migration 000100 and the auth hook store against a real Postgres.

const testHookURL = "https://hooks.example.com/claims"

func TestAuthHooks_UnsetProjectHasNoHooksAndNoSecret(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	got, err := store.ListAuthHooks(ctx, "proj_never_set")
	if err != nil || len(got) != 0 {
		t.Fatalf("list = %+v, %v", got, err)
	}
	if set, err := store.AuthHookSecretSet(ctx, "proj_never_set"); err != nil || set {
		t.Fatalf("secret set = %v, %v", set, err)
	}
}

func TestAuthHooks_EnablingNeedsTheSecretMark(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	saveAuthSettingsProject(t, store, "proj_hooks")
	on := domain.ProjectAuthHook{Hook: domain.AuthHookCustomAccessToken, Enabled: true, URL: testHookURL}
	if err := store.PutAuthHook(ctx, "proj_hooks", on); !errors.Is(err, domain.ErrAuthHookNeedsSecret) {
		t.Fatalf("enable without a secret: %v", err)
	}
	off := domain.ProjectAuthHook{Hook: domain.AuthHookCustomAccessToken, URL: testHookURL}
	if err := store.PutAuthHook(ctx, "proj_hooks", off); err != nil {
		t.Fatalf("save while off: %v", err)
	}
	if err := store.MarkAuthHookSecretSet(ctx, "proj_hooks"); err != nil {
		t.Fatalf("mark: %v", err)
	}
	if err := store.MarkAuthHookSecretSet(ctx, "proj_hooks"); err != nil {
		t.Fatalf("rotating again: %v", err)
	}
	if err := store.PutAuthHook(ctx, "proj_hooks", on); err != nil {
		t.Fatalf("enable with a secret: %v", err)
	}
	got, err := store.ListAuthHooks(ctx, "proj_hooks")
	if err != nil || len(got) != 1 || !got[0].Enabled || got[0].URL != testHookURL || got[0].UpdatedAt.IsZero() {
		t.Fatalf("list = %+v, %v", got, err)
	}
}

func TestAuthHooks_ClearingTheSecretSwitchesEveryHookOffAndIsolates(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	for _, project := range []string{"proj_hooks_a", "proj_hooks_b"} {
		saveAuthSettingsProject(t, store, project)
		if err := store.MarkAuthHookSecretSet(ctx, project); err != nil {
			t.Fatal(err)
		}
		for _, name := range domain.AuthHookNames() {
			if err := store.PutAuthHook(ctx, project, domain.ProjectAuthHook{Hook: name, Enabled: true, URL: testHookURL}); err != nil {
				t.Fatalf("put %s: %v", name, err)
			}
		}
	}
	if err := store.ClearAuthHookSecret(ctx, "proj_hooks_a"); err != nil {
		t.Fatalf("clear: %v", err)
	}
	cleared, _ := store.ListAuthHooks(ctx, "proj_hooks_a")
	for _, hook := range cleared {
		if hook.Enabled || hook.URL != testHookURL {
			t.Errorf("after clear: %+v", hook)
		}
	}
	if set, _ := store.AuthHookSecretSet(ctx, "proj_hooks_a"); set || len(cleared) != 3 {
		t.Fatalf("secret still set=%v, hooks %+v", set, cleared)
	}
	other, _ := store.ListAuthHooks(ctx, "proj_hooks_b")
	if set, _ := store.AuthHookSecretSet(ctx, "proj_hooks_b"); !set || len(other) != 3 || !other[0].Enabled {
		t.Fatalf("the other project was touched: %v %+v", set, other)
	}
}

func TestAuthHooks_TheTableRefusesAnUnknownHookOrAnEnabledEmptyURL(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	saveAuthSettingsProject(t, store, "proj_hooks_check")
	if err := store.MarkAuthHookSecretSet(ctx, "proj_hooks_check"); err != nil {
		t.Fatal(err)
	}
	for _, hook := range []domain.ProjectAuthHook{
		{Hook: "after_sign_in", URL: testHookURL},
		{Hook: domain.AuthHookSendEmail, Enabled: true},
	} {
		if err := store.PutAuthHook(ctx, "proj_hooks_check", hook); err == nil {
			t.Errorf("%+v was stored", hook)
		}
	}
}

func TestAuthHooks_GoWithTheProject(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	saveAuthSettingsProject(t, store, "proj_hooks_gone")
	if err := store.MarkAuthHookSecretSet(ctx, "proj_hooks_gone"); err != nil {
		t.Fatal(err)
	}
	if err := store.PutAuthHook(ctx, "proj_hooks_gone", domain.ProjectAuthHook{Hook: domain.AuthHookSendEmail, Enabled: true, URL: testHookURL}); err != nil {
		t.Fatal(err)
	}
	if err := store.Delete("proj_hooks_gone"); err != nil {
		t.Fatalf("delete project: %v", err)
	}
	got, _ := store.ListAuthHooks(ctx, "proj_hooks_gone")
	if set, _ := store.AuthHookSecretSet(ctx, "proj_hooks_gone"); set || len(got) != 0 {
		t.Fatalf("hooks outlived the project: %v %+v", set, got)
	}
}

// A restored copy has no signing secret in the vault, so it starts with no hooks.
func TestAuthHooks_RestoredCopyDoesNotInheritHooks(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	saveAuthSettingsProject(t, store, "proj_hooks_src")
	saveAuthSettingsProject(t, store, "proj_hooks_copy")
	if err := store.MarkAuthHookSecretSet(ctx, "proj_hooks_src"); err != nil {
		t.Fatal(err)
	}
	if err := store.PutAuthHook(ctx, "proj_hooks_src", domain.ProjectAuthHook{Hook: domain.AuthHookSendEmail, Enabled: true, URL: testHookURL}); err != nil {
		t.Fatal(err)
	}
	if err := store.CopyRestoredSettings(ctx, "proj_hooks_src", "proj_hooks_copy"); err != nil {
		t.Fatalf("copy: %v", err)
	}
	got, _ := store.ListAuthHooks(ctx, "proj_hooks_copy")
	if set, _ := store.AuthHookSecretSet(ctx, "proj_hooks_copy"); set || len(got) != 0 {
		t.Fatalf("the copy inherited hooks: %v %+v", set, got)
	}
}

func TestAuthHooks_SecretLockSerializesOneProjectOnly(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	release, err := store.LockAuthHookSecret(ctx, "proj_lock_a")
	if err != nil {
		t.Fatalf("lock: %v", err)
	}
	other, err := store.LockAuthHookSecret(ctx, "proj_lock_b")
	if err != nil {
		t.Fatalf("another project waited: %v", err)
	}
	other()

	acquired := make(chan func(), 1)
	go func() {
		second, err := store.LockAuthHookSecret(ctx, "proj_lock_a")
		if err != nil {
			t.Errorf("second lock: %v", err)
			close(acquired)
			return
		}
		acquired <- second
	}()
	select {
	case <-acquired:
		t.Fatal("the same project was locked twice")
	case <-time.After(300 * time.Millisecond):
	}
	release()
	select {
	case second := <-acquired:
		if second != nil {
			second()
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the lock was not released")
	}
}
