package apphost_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/apphost"
)

func TestNormalizeCustomDomain(t *testing.T) {
	for raw, want := range map[string]string{
		"shop.example.com":       "shop.example.com",
		"Shop.Example.COM.":      "shop.example.com",
		"a.b.shop.example.co.uk": "a.b.shop.example.co.uk",
		// A pasted URL keeps only its host.
		"https://shop.example.com":           "shop.example.com",
		"http://Shop.Example.com/":           "shop.example.com",
		"https://shop.example.com/a/b?x=1#y": "shop.example.com",
		"shop.example.com/landing":           "shop.example.com",
		// An internationalised name is stored in its ASCII (punycode) form.
		"bücher.example.com": "xn--bcher-kva.example.com",
	} {
		got, err := apphost.NormalizeCustomDomain(raw, "apps.excalibase.io")
		if err != nil || got != want {
			t.Errorf("NormalizeCustomDomain(%q) = %q, %v; want %q", raw, got, err, want)
		}
	}
	for _, bad := range []string{
		"", "example.com", "example.co.uk", "co.uk", "com",
		"*.example.com", "10.0.0.1", "shop example.com", "shop_x.example.com",
		"-shop.example.com", strings.Repeat("a", 64) + ".example.com",
		"web-p1.apps.excalibase.io", "apps.excalibase.io", "https://", "ftp://shop.example.com",
		"shop.example.com:8443", "https://user@shop.example.com",
	} {
		if _, err := apphost.NormalizeCustomDomain(bad, "apps.excalibase.io"); !errors.Is(err, apphost.ErrInvalidDomain) {
			t.Errorf("NormalizeCustomDomain(%q) = %v, want ErrInvalidDomain", bad, err)
		}
	}
}

// A wildcard cannot be proven with one CNAME; it gets its own answer rather
// than "not a host name".
func TestNormalizeCustomDomainExplainsWildcards(t *testing.T) {
	for _, raw := range []string{"*.example.com", "https://*.shop.example.com/"} {
		_, err := apphost.NormalizeCustomDomain(raw, "apps.excalibase.io")
		if !errors.Is(err, apphost.ErrInvalidDomain) || !strings.Contains(err.Error(), "wildcard") {
			t.Errorf("NormalizeCustomDomain(%q) = %v, want a wildcard refusal", raw, err)
		}
	}
}

func TestDomainRoutable(t *testing.T) {
	for status, want := range map[string]bool{
		apphost.DomainPending: false, apphost.DomainIssuing: true, apphost.DomainActive: true,
		apphost.DomainIssueFailed: true, apphost.DomainDetached: false,
	} {
		if got := apphost.DomainRoutable(status); got != want {
			t.Errorf("DomainRoutable(%s) = %v", status, got)
		}
	}
}
