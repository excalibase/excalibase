package config

import (
	"fmt"
	"strings"
	"testing"
)

func setSitesEnv(t *testing.T) {
	t.Helper()
	t.Setenv("SITES_ENABLED", "true")
	t.Setenv("SITES_CLOUDFLARE_ACCOUNT_ID", "acct123")
	t.Setenv("SITES_CLOUDFLARE_API_TOKEN", "cf-token")
	t.Setenv("SITES_DISPATCH_NAMESPACE", "excalibase-sites")
	t.Setenv("SITES_BASE_DOMAIN", "excalibase.app")
}

func TestSites_ReadItsSettings(t *testing.T) {
	setSitesEnv(t)
	cfg := Load()
	got := cfg.Sites
	if !cfg.SitesEnabled || got.AccountID != "acct123" || got.APIToken != "cf-token" ||
		got.DispatchNamespace != "excalibase-sites" || got.BaseDomain != "excalibase.app" {
		t.Fatalf("sites settings: %+v", got)
	}
	if !cfg.SitesAvailable() {
		t.Fatal("a fully configured install must offer Sites")
	}
}

func TestSites_OffUnlessSwitchedOnWithEverySetting(t *testing.T) {
	complete := SitesConfig{AccountID: "a", APIToken: "t", DispatchNamespace: "n", BaseDomain: "excalibase.app"}
	if (AppConfig{Sites: complete}).SitesAvailable() {
		t.Error("settings without SITES_ENABLED offered Sites")
	}
	missingToken := complete
	missingToken.APIToken = ""
	if (AppConfig{SitesEnabled: true, Sites: missingToken}).SitesAvailable() {
		t.Error("a missing token offered Sites")
	}
}

func TestSites_TokenNeverFormatted(t *testing.T) {
	settings := SitesConfig{AccountID: "a", APIToken: "cf-secret-token", DispatchNamespace: "n", BaseDomain: "excalibase.app"}
	for _, verb := range []string{"%v", "%+v", "%s", "%#v"} {
		if strings.Contains(sprintf(verb, settings), "cf-secret-token") {
			t.Errorf("%s prints the API token", verb)
		}
	}
}

func TestValidateSites_RefusesABaseDomainThatIsNotAHostname(t *testing.T) {
	for _, domain := range []string{"https://excalibase.app", "excalibase.app/", "localhost", "-bad.app"} {
		cfg := AppConfig{SitesEnabled: true, Sites: SitesConfig{AccountID: "a", APIToken: "t", DispatchNamespace: "n", BaseDomain: domain}}
		if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "SITES_BASE_DOMAIN") {
			t.Errorf("%q: err = %v", domain, err)
		}
	}
}

func sprintf(verb string, value any) string { return fmt.Sprintf(verb, value) }
