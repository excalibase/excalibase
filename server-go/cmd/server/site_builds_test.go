package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/config"
	custommw "github.com/excalibase/provisioning-poc/internal/middleware"
	"github.com/excalibase/provisioning-poc/internal/sitebuilds"
)

// The sign-in callback's code and state are credentials; the access log keeps neither.
func TestAccessLogDropsTheGitSignInCode(t *testing.T) {
	var logged string
	handler := custommw.RedactQueryParams(accessLogRedactedParams...)(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		logged = r.RequestURI
	}))
	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/api/sites/github/callback?code=gho_secret&state=abc123", nil))
	if strings.Contains(logged, "gho_secret") || strings.Contains(logged, "abc123") {
		t.Fatalf("logged %s", logged)
	}
}

func TestSiteBuildMode(t *testing.T) {
	on := config.AppConfig{SitesEnabled: true, SitesGitBuildsEnabled: true}
	on.Sites = config.SitesConfig{AccountID: "a", APIToken: "t", DispatchNamespace: "n", BaseDomain: "example.app", HostsKVNamespaceID: "kv"}
	if !on.SiteGitBuildsOn() {
		t.Fatal("the fixture must count as Sites available with git builds on")
	}
	cases := map[string]struct {
		cfg  config.AppConfig
		want sitebuilds.Mode
	}{
		"off by default":       {config.AppConfig{}, sitebuilds.ModeOff},
		"on in a cluster":      {on, sitebuilds.ModeOn},
		"named on single-host": {withMode(on, "docker"), sitebuilds.ModeSingleHost},
	}
	for name, tc := range cases {
		if got := siteBuildMode(tc.cfg); got != tc.want {
			t.Errorf("%s: %v, want %v", name, got, tc.want)
		}
	}
}

func withMode(cfg config.AppConfig, mode string) config.AppConfig {
	cfg.ProvisionerMode = mode
	return cfg
}

// Studio calls /api on its own origin, where the sign-in cookie is set; GitHub must send the browser back there.
func TestSiteGitCallbackURL_IsOnTheStudioOrigin(t *testing.T) {
	cfg := config.AppConfig{StudioURL: "https://admin.example.com/", PublicBaseURL: "https://api.example.com"}
	if got := siteGitCallbackURL(cfg); got != "https://admin.example.com/api/sites/github/callback" {
		t.Fatalf("callback %s", got)
	}
}
