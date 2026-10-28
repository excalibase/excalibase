//go:build integration

package postgres

import (
	"context"
	"fmt"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/domain"
)

// Exercises migration 000084 and the provider and sign-in policy stores
// against a real Postgres (New runs migrations on connect).

func TestAuthProviders_UnsetProjectListsNothing(t *testing.T) {
	store := testStore(t)
	got, err := store.ListAuthProviders(context.Background(), "proj_never_set")
	if err != nil || len(got) != 0 {
		t.Fatalf("list = %+v, %v", got, err)
	}
}

func TestAuthProviders_PutListReplaceAndIsolate(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	saveAuthSettingsProject(t, store, "proj_oauth_a")
	saveAuthSettingsProject(t, store, "proj_oauth_b")

	first := domain.ProjectAuthProvider{Provider: domain.AuthProviderGitHub, Enabled: true, ClientID: "gh-1", ClientSecretSet: true}
	if err := store.PutAuthProvider(ctx, "proj_oauth_a", first); err != nil {
		t.Fatalf("put: %v", err)
	}
	second := domain.ProjectAuthProvider{Provider: domain.AuthProviderGitHub, Enabled: false, ClientID: "gh-2", ClientSecretSet: true}
	if err := store.PutAuthProvider(ctx, "proj_oauth_a", second); err != nil {
		t.Fatalf("replace: %v", err)
	}
	got, err := store.ListAuthProviders(ctx, "proj_oauth_a")
	if err != nil || len(got) != 1 {
		t.Fatalf("list = %+v, %v", got, err)
	}
	row := got[0]
	if row.Provider != "github" || row.Enabled || row.ClientID != "gh-2" || !row.ClientSecretSet || row.UpdatedAt.IsZero() {
		t.Fatalf("row = %+v", row)
	}
	if other, _ := store.ListAuthProviders(ctx, "proj_oauth_b"); len(other) != 0 {
		t.Fatalf("project b sees project a's providers: %+v", other)
	}
}

func TestAuthProviders_RefusesUnknownProviderAndProject(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	saveAuthSettingsProject(t, store, "proj_oauth_c")
	if err := store.PutAuthProvider(ctx, "proj_oauth_c", domain.ProjectAuthProvider{Provider: "apple"}); err == nil {
		t.Error("the CHECK constraint must refuse a provider the platform does not support")
	}
	if err := store.PutAuthProvider(ctx, "ghost", domain.ProjectAuthProvider{Provider: "google"}); err == nil {
		t.Error("a row for a project that does not exist must fail (FK)")
	}
}

func TestAuthProviders_CascadeDeleteWithInstance(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	saveAuthSettingsProject(t, store, "proj_oauth_gone")
	if err := store.PutAuthProvider(ctx, "proj_oauth_gone", domain.ProjectAuthProvider{Provider: "google", ClientID: "g"}); err != nil {
		t.Fatal(err)
	}
	if err := store.Delete("proj_oauth_gone"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if got, _ := store.ListAuthProviders(ctx, "proj_oauth_gone"); len(got) != 0 {
		t.Fatalf("providers must cascade-delete with the project: %+v", got)
	}
}

func TestAuthSignIn_DefaultsWithoutARowAndForAnOlderRow(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	got, err := store.GetAuthSignIn(ctx, "proj_never_set")
	if err != nil || !got.AllowNewUsers || got.RedirectURLs == nil || len(got.RedirectURLs) != 0 || got.EmailOTPEnabled {
		t.Fatalf("no row = %+v, %v", got, err)
	}
	// A row written by the flag-off path still reads with the column defaults.
	saveAuthSettingsProject(t, store, "proj_signin_old")
	if err := store.SetAuthSettings(ctx, "proj_signin_old", domain.ProjectAuthSettings{SiteURL: "https://a.example.com"}); err != nil {
		t.Fatal(err)
	}
	got, err = store.GetAuthSignIn(ctx, "proj_signin_old")
	if err != nil || !got.AllowNewUsers || got.RedirectURLs == nil || len(got.RedirectURLs) != 0 || got.EmailOTPEnabled {
		t.Fatalf("older row = %+v, %v", got, err)
	}
}

func TestAuthSignIn_RoundTripAndTheOldWriterKeepsIt(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	saveAuthSettingsProject(t, store, "proj_signin")
	settings := domain.ProjectAuthSettings{RequireEmailVerification: true, SiteURL: "https://app.example.com"}
	signIn := domain.ProjectAuthSignIn{RedirectURLs: []string{"https://app.example.com/cb", "http://localhost:3000/cb"}, AllowNewUsers: false, EmailOTPEnabled: true}
	if err := store.SetAuthSettingsWithSignIn(ctx, "proj_signin", settings, signIn); err != nil {
		t.Fatalf("set: %v", err)
	}
	got, err := store.GetAuthSignIn(ctx, "proj_signin")
	if err != nil || got.AllowNewUsers || fmt.Sprint(got.RedirectURLs) != fmt.Sprint(signIn.RedirectURLs) || !got.EmailOTPEnabled {
		t.Fatalf("round trip = %+v, %v", got, err)
	}
	if stored, ok, _ := store.GetAuthSettings(ctx, "proj_signin"); !ok || stored != settings {
		t.Fatalf("settings = %+v", stored)
	}
	// The flag-off writer touches only its two columns.
	if err := store.SetAuthSettings(ctx, "proj_signin", domain.ProjectAuthSettings{SiteURL: "https://b.example.com"}); err != nil {
		t.Fatal(err)
	}
	if got, _ := store.GetAuthSignIn(ctx, "proj_signin"); got.AllowNewUsers || len(got.RedirectURLs) != 2 || !got.EmailOTPEnabled {
		t.Fatalf("the original writer wiped the policy: %+v", got)
	}
}

func TestAuthSignIn_RestoredCopyCarriesThePolicy(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	saveAuthSettingsProject(t, store, "proj_signin_src")
	saveAuthSettingsProject(t, store, "proj_signin_copy")
	signIn := domain.ProjectAuthSignIn{RedirectURLs: []string{"https://app.example.com/cb"}, AllowNewUsers: false, EmailOTPEnabled: true}
	if err := store.SetAuthSettingsWithSignIn(ctx, "proj_signin_src", domain.ProjectAuthSettings{SiteURL: "https://app.example.com"}, signIn); err != nil {
		t.Fatal(err)
	}
	if err := store.PutAuthProvider(ctx, "proj_signin_src", domain.ProjectAuthProvider{Provider: "github", Enabled: true, ClientID: "gh", ClientSecretSet: true}); err != nil {
		t.Fatal(err)
	}
	if err := store.CopyRestoredSettings(ctx, "proj_signin_src", "proj_signin_copy"); err != nil {
		t.Fatalf("copy: %v", err)
	}
	got, err := store.GetAuthSignIn(ctx, "proj_signin_copy")
	if err != nil || got.AllowNewUsers || fmt.Sprint(got.RedirectURLs) != "[https://app.example.com/cb]" || !got.EmailOTPEnabled {
		t.Fatalf("copy policy = %+v, %v", got, err)
	}
	// The copy has its own callback URL and no secret in the vault, so its
	// providers start unconfigured.
	if providers, _ := store.ListAuthProviders(ctx, "proj_signin_copy"); len(providers) != 0 {
		t.Fatalf("providers copied: %+v", providers)
	}
}

func TestAuthSignIn_GuestSwitchAndCaptchaAreStoredApart(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	saveAuthSettingsProject(t, store, "proj_guest")
	// The captcha column may be written before any settings row exists.
	if err := store.SetGuestCaptchaSecretSet(ctx, "proj_guest", true); err != nil {
		t.Fatalf("captcha: %v", err)
	}
	signIn := domain.ProjectAuthSignIn{RedirectURLs: []string{}, AllowNewUsers: true, GuestSignInEnabled: true}
	if err := store.SetAuthSettingsWithSignIn(ctx, "proj_guest", domain.ProjectAuthSettings{}, signIn); err != nil {
		t.Fatalf("set: %v", err)
	}
	got, err := store.GetAuthSignIn(ctx, "proj_guest")
	if err != nil || !got.GuestSignInEnabled || !got.GuestCaptchaSecretSet || !got.AllowNewUsers {
		t.Fatalf("a settings write must keep the captcha column: %+v, %v", got, err)
	}
	if err := store.SetGuestCaptchaSecretSet(ctx, "proj_guest", false); err != nil {
		t.Fatal(err)
	}
	if got, _ := store.GetAuthSignIn(ctx, "proj_guest"); got.GuestCaptchaSecretSet || !got.GuestSignInEnabled {
		t.Fatalf("clearing the captcha touched the switch: %+v", got)
	}
}

func TestAuthSignIn_RestoredCopyTakesGuestsOnlyWithoutACaptcha(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	for i, withCaptcha := range []bool{false, true} {
		src, dst := fmt.Sprintf("proj_guest_src%d", i), fmt.Sprintf("proj_guest_copy%d", i)
		saveAuthSettingsProject(t, store, src)
		saveAuthSettingsProject(t, store, dst)
		signIn := domain.ProjectAuthSignIn{RedirectURLs: []string{}, AllowNewUsers: true, GuestSignInEnabled: true}
		if err := store.SetAuthSettingsWithSignIn(ctx, src, domain.ProjectAuthSettings{}, signIn); err != nil {
			t.Fatal(err)
		}
		if err := store.SetGuestCaptchaSecretSet(ctx, src, withCaptcha); err != nil {
			t.Fatal(err)
		}
		if err := store.CopyRestoredSettings(ctx, src, dst); err != nil {
			t.Fatalf("copy: %v", err)
		}
		got, err := store.GetAuthSignIn(ctx, dst)
		if err != nil || got.GuestCaptchaSecretSet || got.GuestSignInEnabled == withCaptcha {
			t.Fatalf("captcha=%v: copy = %+v, %v", withCaptcha, got, err)
		}
	}
}

func TestAuthProviders_DeleteRemovesOneRowOnly(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	saveAuthSettingsProject(t, store, "proj_oauth_del")
	for _, name := range []string{domain.AuthProviderGitHub, domain.AuthProviderGoogle} {
		if err := store.PutAuthProvider(ctx, "proj_oauth_del", domain.ProjectAuthProvider{Provider: name, Enabled: true, ClientID: "id", ClientSecretSet: true}); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.DeleteAuthProvider(ctx, "proj_oauth_del", domain.AuthProviderGitHub); err != nil {
		t.Fatalf("delete: %v", err)
	}
	got, err := store.ListAuthProviders(ctx, "proj_oauth_del")
	if err != nil || len(got) != 1 || got[0].Provider != domain.AuthProviderGoogle {
		t.Fatalf("after delete = %+v, %v", got, err)
	}
	if err := store.DeleteAuthProvider(ctx, "proj_oauth_del", domain.AuthProviderGitHub); err != nil {
		t.Fatalf("deleting a missing row: %v", err)
	}
}
