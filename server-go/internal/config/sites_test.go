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
	t.Setenv("SITES_HOSTS_KV_NAMESPACE_ID", "kv-hosts-1")
}

func TestSites_ReadItsSettings(t *testing.T) {
	setSitesEnv(t)
	cfg := Load()
	got := cfg.Sites
	if !cfg.SitesEnabled || got.AccountID != "acct123" || got.APIToken != "cf-token" ||
		got.DispatchNamespace != "excalibase-sites" || got.BaseDomain != "excalibase.app" || got.HostsKVNamespaceID != "kv-hosts-1" {
		t.Fatalf("sites settings: %+v", got)
	}
	if !cfg.SitesAvailable() {
		t.Fatal("a fully configured install must offer Sites")
	}
}

func TestSites_OffUnlessSwitchedOnWithEverySetting(t *testing.T) {
	complete := SitesConfig{AccountID: "a", APIToken: "t", DispatchNamespace: "n", BaseDomain: "excalibase.app", HostsKVNamespaceID: "k"}
	if (AppConfig{Sites: complete}).SitesAvailable() {
		t.Error("settings without SITES_ENABLED offered Sites")
	}
	if !(AppConfig{SitesEnabled: true, Sites: complete}).SitesAvailable() {
		t.Error("complete settings did not offer Sites")
	}
	missingHosts := complete
	missingHosts.HostsKVNamespaceID = ""
	if (AppConfig{SitesEnabled: true, Sites: missingHosts}).SitesAvailable() {
		t.Error("a missing hosts KV namespace offered Sites")
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

// Apps are <app>-<id>.<APP_DOMAIN>; a site under the same domain could take an app's hostname.
func TestValidateSites_RefusesABaseDomainOverlappingTheAppDomain(t *testing.T) {
	sites := SitesConfig{AccountID: "a", APIToken: "t", DispatchNamespace: "n"}
	for _, pair := range [][2]string{
		{"excalibase.app", "excalibase.app"},
		{"sites.excalibase.app", "excalibase.app"},
		{"excalibase.app", "apps.excalibase.app"},
	} {
		sites.BaseDomain = pair[0]
		cfg := AppConfig{SitesEnabled: true, Sites: sites, AppDomain: pair[1]}
		if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "APP_DOMAIN") {
			t.Errorf("sites %q, apps %q: err = %v", pair[0], pair[1], err)
		}
	}
	sites.BaseDomain = "excalibase.app"
	cfg := AppConfig{SitesEnabled: true, Sites: sites, AppDomain: "notexcalibase.app"}
	if err := cfg.validateSites(); err != nil {
		t.Errorf("unrelated domains refused: %v", err)
	}
}

// Custom domains (EXC-614) need the zone id on top of Sites; the CNAME target defaults under the base domain.
func TestSites_CustomDomainsNeedTheZone(t *testing.T) {
	setSitesEnv(t)
	cfg := Load()
	if cfg.SitesCustomDomainsAvailable() {
		t.Fatal("custom domains offered without a zone id")
	}
	t.Setenv("SITES_ZONE_ID", "0123456789abcdef0123456789abcdef")
	cfg = Load()
	if !cfg.SitesCustomDomainsAvailable() || cfg.Sites.CustomDomainTarget() != "cname.excalibase.app" {
		t.Fatalf("custom domains: available=%v target=%q", cfg.SitesCustomDomainsAvailable(), cfg.Sites.CustomDomainTarget())
	}
	t.Setenv("SITES_CUSTOM_DOMAIN_TARGET", "Customers.Excalibase.App")
	cfg = Load()
	if cfg.Sites.CustomDomainTarget() != "customers.excalibase.app" {
		t.Fatalf("target=%q", cfg.Sites.CustomDomainTarget())
	}
}

func TestValidateSites_RefusesABadZoneOrTarget(t *testing.T) {
	base := SitesConfig{AccountID: "a", APIToken: "t", DispatchNamespace: "n", BaseDomain: "excalibase.app", HostsKVNamespaceID: "k"}
	for name, change := range map[string]func(*SitesConfig){
		"SITES_ZONE_ID": func(s *SitesConfig) { s.ZoneID = "excalibase.app" },
		"SITES_CUSTOM_DOMAIN_TARGET": func(s *SitesConfig) {
			s.ZoneID, s.CustomTarget = "0123456789abcdef0123456789abcdef", "cname.example.com"
		},
	} {
		settings := base
		change(&settings)
		err := (AppConfig{SitesEnabled: true, Sites: settings}).Validate()
		if err == nil || !strings.Contains(err.Error(), name) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
}
