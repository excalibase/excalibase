//go:build integration

package postgres

import (
	"context"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/domain"
)

// EXC-662 step 7: the MFA switch is its own column; the caller makes sure the
// vault holds the project's key first.

func TestAuthMFA_EnableRunsTheKeyStepFirstAndKeepsOtherSettings(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	saveAuthSettingsProject(t, store, "proj_mfa_a")
	signIn := domain.ProjectAuthSignIn{RedirectURLs: []string{"https://app.example.com/cb"}, AllowNewUsers: false, GuestSignInEnabled: true}
	if err := store.SetAuthSettingsWithSignIn(ctx, "proj_mfa_a", domain.ProjectAuthSettings{SiteURL: "https://app.example.com"}, signIn); err != nil {
		t.Fatal(err)
	}
	if err := store.EnableMFA(ctx, "proj_mfa_a"); err != nil {
		t.Fatalf("enable: %v", err)
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
	if err := store.EnableMFA(ctx, "proj_mfa_b"); err != nil {
		t.Fatal(err)
	}
	got, err := store.GetAuthSignIn(ctx, "proj_mfa_b")
	if err != nil || !got.MFAEnabled || !got.AllowNewUsers {
		t.Fatalf("got %+v, %v", got, err)
	}
}

func TestAuthMFA_DisablingAProjectWithNoRowIsNoError(t *testing.T) {
	store := testStore(t)
	if err := store.DisableMFA(context.Background(), "proj_mfa_never"); err != nil {
		t.Fatal(err)
	}
}

func TestAuthMFA_RestoredCopyCarriesTheSwitch(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	saveAuthSettingsProject(t, store, "proj_mfa_src")
	saveAuthSettingsProject(t, store, "proj_mfa_copy")
	if err := store.EnableMFA(ctx, "proj_mfa_src"); err != nil {
		t.Fatal(err)
	}
	if err := store.CopyRestoredSettings(ctx, "proj_mfa_src", "proj_mfa_copy"); err != nil {
		t.Fatal(err)
	}
	if got, _ := store.GetAuthSignIn(ctx, "proj_mfa_copy"); !got.MFAEnabled {
		t.Fatalf("copy = %+v", got)
	}
}
