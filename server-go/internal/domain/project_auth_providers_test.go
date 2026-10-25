package domain

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestAuthProviderNames_AreGoogleThenGitHub(t *testing.T) {
	got := AuthProviderNames()
	if len(got) != 2 || got[0] != AuthProviderGoogle || got[1] != AuthProviderGitHub {
		t.Fatalf("providers = %v", got)
	}
	if !IsAuthProvider("github") || IsAuthProvider("apple") || IsAuthProvider("") {
		t.Fatal("only google and github are providers")
	}
}

func TestAuthProviderCallbackURL(t *testing.T) {
	cases := map[string]string{
		"https://api.example.com":   "https://api.example.com/auth/acme/proj-1/callback/github",
		"https://api.example.com/":  "https://api.example.com/auth/acme/proj-1/callback/github",
		"https://api.example.com//": "https://api.example.com/auth/acme/proj-1/callback/github",
	}
	for base, want := range cases {
		if got := AuthProviderCallbackURL(base, "acme", "proj-1", "github"); got != want {
			t.Errorf("base %q: got %q want %q", base, got, want)
		}
	}
}

func TestAuthProviderSecretPath(t *testing.T) {
	if got := AuthProviderSecretPath("proj-1", "google"); got != "projects/proj-1/auth/providers/google" {
		t.Fatalf("secret path = %q", got)
	}
}

func TestValidateAuthProviderClientID(t *testing.T) {
	got, err := ValidateAuthProviderClientID("  1234-abc.apps.googleusercontent.com \n")
	if err != nil || got != "1234-abc.apps.googleusercontent.com" {
		t.Fatalf("trimmed id = %q, %v", got, err)
	}
	if got, err := ValidateAuthProviderClientID("   "); err != nil || got != "" {
		t.Fatalf("blank id reads as unset: %q, %v", got, err)
	}
	for _, bad := range []string{"has space", "tab\there", "nul\x00", "del\x7f", strings.Repeat("a", 257)} {
		if _, err := ValidateAuthProviderClientID(bad); !errors.Is(err, ErrInvalidAuthProvider) {
			t.Errorf("%q accepted: %v", bad, err)
		}
	}
	if _, err := ValidateAuthProviderClientID(strings.Repeat("a", 256)); err != nil {
		t.Errorf("256 characters refused: %v", err)
	}
}

func TestValidateAuthProviderClientSecret(t *testing.T) {
	for _, good := range []string{"s", "a secret with spaces", strings.Repeat("x", 512)} {
		if err := ValidateAuthProviderClientSecret(good); err != nil {
			t.Errorf("%q refused: %v", good, err)
		}
	}
	for _, bad := range []string{"", "line\n", "nul\x00", strings.Repeat("x", 513)} {
		if err := ValidateAuthProviderClientSecret(bad); !errors.Is(err, ErrInvalidAuthProvider) {
			t.Errorf("%q accepted: %v", bad, err)
		}
	}
}

func TestValidateAuthProviderClientSecret_NeverEchoesTheSecret(t *testing.T) {
	err := ValidateAuthProviderClientSecret("top-secret\n")
	if err == nil || strings.Contains(err.Error(), "top-secret") {
		t.Fatalf("error must not carry the secret: %v", err)
	}
}

func TestNormalizeRedirectURLs(t *testing.T) {
	got, err := NormalizeRedirectURLs([]string{" HTTPS://App.Example.com/auth/callback ", "https://app.example.com/auth/callback", "http://localhost:3000/cb"})
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	want := []string{"https://app.example.com/auth/callback", "http://localhost:3000/cb"}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("got %v want %v", got, want)
	}
	if got, err := NormalizeRedirectURLs(nil); err != nil || got == nil || len(got) != 0 {
		t.Fatalf("nil list must normalise to an empty, non-nil list: %v %v", got, err)
	}
}

func TestNormalizeRedirectURLs_KeepsThePathEscaped(t *testing.T) {
	got, err := NormalizeRedirectURLs([]string{"https://app.example.com/a%20b/cb"})
	if err != nil || got[0] != "https://app.example.com/a%20b/cb" {
		t.Fatalf("got %v %v", got, err)
	}
}

func TestNormalizeRedirectURLs_Refusals(t *testing.T) {
	tooMany := make([]string, 0, MaxRedirectURLs+1)
	for i := 0; i <= MaxRedirectURLs; i++ {
		tooMany = append(tooMany, fmt.Sprintf("https://app%d.example.com", i))
	}
	for name, list := range map[string][]string{
		"empty entry":       {"https://a.example.com", "  "},
		"query":             {"https://a.example.com/cb?x=1"},
		"plain http":        {"http://a.example.com/cb"},
		"trailing /":        {"https://a.example.com/"},
		"wildcard host":     {"https://*.example.com/cb"},
		"dot segment":       {"https://a.example.com/a/../b"},
		"encoded dot slash": {"https://a.example.com/a%2F..%2Fb"},
		"encoded CR LF":     {"https://a.example.com/%0d%0a"},
		"port out of range": {"https://a.example.com:99999/x"},
		"over the cap":      tooMany,
	} {
		if _, err := NormalizeRedirectURLs(list); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	exactCap := tooMany[:MaxRedirectURLs]
	if _, err := NormalizeRedirectURLs(exactCap); err != nil {
		t.Errorf("%d entries refused: %v", MaxRedirectURLs, err)
	}
}

func TestDefaultAuthSignIn(t *testing.T) {
	got := DefaultAuthSignIn()
	if !got.AllowNewUsers || got.RedirectURLs == nil || len(got.RedirectURLs) != 0 {
		t.Fatalf("default = %+v", got)
	}
}

func TestUsableOAuthProviders(t *testing.T) {
	stored := []ProjectAuthProvider{
		{Provider: AuthProviderGoogle, Enabled: true, ClientID: "g", ClientSecretSet: false},
		{Provider: AuthProviderGitHub, Enabled: true, ClientID: "gh", ClientSecretSet: true},
	}
	got := UsableOAuthProviders(stored, "https://api.example.com", "acme", "proj-1")
	if len(got) != 1 || got[0].Provider != "github" || got[0].ClientID != "gh" ||
		got[0].CallbackURL != "https://api.example.com/auth/acme/proj-1/callback/github" {
		t.Fatalf("usable = %+v", got)
	}
	for _, p := range []ProjectAuthProvider{
		{Provider: AuthProviderGitHub, Enabled: false, ClientID: "gh", ClientSecretSet: true},
		{Provider: AuthProviderGitHub, Enabled: true, ClientID: "", ClientSecretSet: true},
	} {
		if got := UsableOAuthProviders([]ProjectAuthProvider{p}, "https://x", "o", "p"); got == nil || len(got) != 0 {
			t.Errorf("%+v listed: %+v", p, got)
		}
	}
}

func TestAllAuthProviders_FillsAndOrders(t *testing.T) {
	got := AllAuthProviders([]ProjectAuthProvider{{Provider: AuthProviderGitHub, Enabled: true, ClientID: "gh"}, {Provider: "apple"}})
	if len(got) != 2 || got[0].Provider != "google" || got[0].Enabled || got[1].Provider != "github" || got[1].ClientID != "gh" {
		t.Fatalf("all = %+v", got)
	}
}

func TestUsableOAuthProviders_ListsInProviderOrder(t *testing.T) {
	stored := []ProjectAuthProvider{
		{Provider: AuthProviderGitHub, Enabled: true, ClientID: "gh", ClientSecretSet: true},
		{Provider: AuthProviderGoogle, Enabled: true, ClientID: "g", ClientSecretSet: true},
	}
	got := UsableOAuthProviders(stored, "https://x", "o", "p")
	if len(got) != 2 || got[0].Provider != "google" || got[1].Provider != "github" {
		t.Fatalf("order = %+v", got)
	}
}
