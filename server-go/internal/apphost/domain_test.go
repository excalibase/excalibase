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
		"web-p1.apps.excalibase.io", "apps.excalibase.io", "https://shop.example.com",
	} {
		if _, err := apphost.NormalizeCustomDomain(bad, "apps.excalibase.io"); !errors.Is(err, apphost.ErrInvalidDomain) {
			t.Errorf("NormalizeCustomDomain(%q) = %v, want ErrInvalidDomain", bad, err)
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
