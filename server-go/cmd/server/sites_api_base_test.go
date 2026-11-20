package main

import (
	"testing"

	"github.com/excalibase/provisioning-poc/internal/config"
)

// A trailing slash would make the code's paths start with "//", which the outbound Worker refuses.
func TestSiteAPIBaseURLHasNoTrailingSlash(t *testing.T) {
	for in, want := range map[string]string{
		"https://api.excalibase.io":    "https://api.excalibase.io",
		"https://api.excalibase.io/":   "https://api.excalibase.io",
		"https://api.excalibase.io///": "https://api.excalibase.io",
	} {
		if got := siteAPIBaseURL(config.AppConfig{PublicBaseURL: in}); got != want {
			t.Errorf("%q -> %q, want %q", in, got, want)
		}
	}
}
