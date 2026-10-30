//go:build integration

package postgres

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
)

// EXC-662 step 7: the MFA switch is its own column, written only after the
// caller has made sure the vault holds the project's key.

func TestAuthMFA_EnableRunsTheKeyStepFirstAndKeepsOtherSettings(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	saveAuthSettingsProject(t, store, "proj_mfa_a")
	signIn := domain.ProjectAuthSignIn{RedirectURLs: []string{"https://app.example.com/cb"}, AllowNewUsers: false, GuestSignInEnabled: true}
	if err := store.SetAuthSettingsWithSignIn(ctx, "proj_mfa_a", domain.ProjectAuthSettings{SiteURL: "https://app.example.com"}, signIn); err != nil {
		t.Fatal(err)
	}
	ran := false
	if err := store.EnableMFA(ctx, "proj_mfa_a", func() error { ran = true; return nil }); err != nil || !ran {
		t.Fatalf("enable: ran=%v err=%v", ran, err)
	}
	got, err := store.GetAuthSignIn(ctx, "proj_mfa_a")
	if err != nil || !got.MFAEnabled || !got.GuestSignInEnabled || got.AllowNewUsers || len(got.RedirectURLs) != 1 {
		t.Fatalf("after enable: %+v, %v", got, err)
	}
	// A later settings write leaves the switch alone.
	if err := store.SetAuthSettingsWithSignIn(ctx, "proj_mfa_a", domain.ProjectAuthSettings{}, got); err != nil {
		t.Fatal(err)
	}
	if again, _ := store.GetAuthSignIn(ctx, "proj_mfa_a"); !again.MFAEnabled {
		t.Fatal("a settings write turned MFA off")
	}
	if err := store.DisableMFA(ctx, "proj_mfa_a"); err != nil {
		t.Fatal(err)
	}
	if off, _ := store.GetAuthSignIn(ctx, "proj_mfa_a"); off.MFAEnabled || !off.GuestSignInEnabled {
		t.Fatalf("after disable: %+v", off)
	}
}

func TestAuthMFA_EnableWithoutASettingsRowCreatesOneWithDefaults(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	saveAuthSettingsProject(t, store, "proj_mfa_b")
	if got, _ := store.GetAuthSignIn(ctx, "proj_mfa_b"); got.MFAEnabled {
		t.Fatal("a project with no row has MFA on")
	}
	if err := store.EnableMFA(ctx, "proj_mfa_b", func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	got, err := store.GetAuthSignIn(ctx, "proj_mfa_b")
	if err != nil || !got.MFAEnabled || !got.AllowNewUsers {
		t.Fatalf("got %+v, %v", got, err)
	}
}

func TestAuthMFA_AFailedKeyStepLeavesTheSwitchOff(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	saveAuthSettingsProject(t, store, "proj_mfa_c")
	refused := errors.New("vault sealed")
	if err := store.EnableMFA(ctx, "proj_mfa_c", func() error { return refused }); !errors.Is(err, refused) {
		t.Fatalf("enable = %v", err)
	}
	if got, _ := store.GetAuthSignIn(ctx, "proj_mfa_c"); got.MFAEnabled {
		t.Fatal("the switch is on without a key")
	}
	// Disabling a project that has no row is no error.
	if err := store.DisableMFA(ctx, "proj_mfa_never"); err != nil {
		t.Fatal(err)
	}
}

// Two enables at once must not both find the key missing and both write one.
func TestAuthMFA_ConcurrentEnablesRunTheKeyStepOneAtATime(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	saveAuthSettingsProject(t, store, "proj_mfa_d")
	var mu sync.Mutex
	inside, overlapped := 0, false
	step := func() error {
		mu.Lock()
		inside++
		overlapped = overlapped || inside > 1
		mu.Unlock()
		time.Sleep(150 * time.Millisecond)
		mu.Lock()
		inside--
		mu.Unlock()
		return nil
	}
	var wg sync.WaitGroup
	errs := make([]error, 3)
	for i := range errs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs[i] = store.EnableMFA(ctx, "proj_mfa_d", step)
		}()
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if overlapped {
		t.Fatal("two key steps ran at once for one project")
	}
}

func TestAuthMFA_RestoredCopyCarriesTheSwitch(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	saveAuthSettingsProject(t, store, "proj_mfa_src")
	saveAuthSettingsProject(t, store, "proj_mfa_copy")
	if err := store.EnableMFA(ctx, "proj_mfa_src", func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	if err := store.CopyRestoredSettings(ctx, "proj_mfa_src", "proj_mfa_copy"); err != nil {
		t.Fatal(err)
	}
	if got, _ := store.GetAuthSignIn(ctx, "proj_mfa_copy"); !got.MFAEnabled {
		t.Fatalf("copy = %+v", got)
	}
}
