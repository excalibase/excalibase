package sites

import (
	"strings"
	"testing"
)

func TestValidateNameAcceptsDNSLabels(t *testing.T) {
	for _, name := range []string{"shop", "my-shop", "a1b", "clash-of-pokemon", strings.Repeat("a", 63)} {
		if err := ValidateName(name); err != nil {
			t.Errorf("ValidateName(%q) = %v", name, err)
		}
	}
}

func TestValidateNameRefuses(t *testing.T) {
	for _, name := range []string{
		"", "ab", "Shop", "-shop", "shop-", "my_shop", "my.shop", "xn--shop", "a--b",
		strings.Repeat("a", 64), "www", "api", "admin", "studio",
		"ns", "ns1", "ns2", "mx", "smtp", "imap", "pop", "webmail", "autodiscover", "autoconfig", "static", "assets",
		"cdn", "sso", "oauth", "auth", "id", "account", "accounts", "security", "help", "support", "blog", "docs",
		"status", "graphql", "rest", "realtime", "storage", "functions", "edge", "gateway", "internal", "staging",
		"dev", "beta", "app", "apps", "dashboard", "console", "billing", "login",
		"excalibase", "my-excalibase-app", "excalibase-login",
	} {
		if err := ValidateName(name); err == nil {
			t.Errorf("ValidateName(%q) accepted", name)
		}
	}
}

func TestValidateLabel(t *testing.T) {
	if err := ValidateLabel("main@3f2a1bc"); err != nil {
		t.Errorf("label refused: %v", err)
	}
	if err := ValidateLabel(""); err != nil {
		t.Errorf("empty label refused: %v", err)
	}
	if err := ValidateLabel(strings.Repeat("x", 201)); err == nil {
		t.Error("an overlong label was accepted")
	}
	if err := ValidateLabel("line\nbreak"); err == nil {
		t.Error("a control character was accepted")
	}
}

func TestScriptAndOwnerNamesFollowTheSiteID(t *testing.T) {
	site := &Site{ID: "0b6f2c9e-6a51-4c07-9a3c-7f1d2e3a4b5c"}
	if ScriptName(site) != "site-0b6f2c9e-6a51-4c07-9a3c-7f1d2e3a4b5c" {
		t.Errorf("script = %q", ScriptName(site))
	}
	if CorsOwner(site) != "site-0b6f2c9e-6a51-4c07-9a3c-7f1d2e3a4b5c" {
		t.Errorf("owner = %q", CorsOwner(site))
	}
}
