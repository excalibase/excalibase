package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/docker/docker/client"

	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/features"
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
		"off by default":              {config.AppConfig{}, sitebuilds.ModeOff},
		"on in a cluster":             {on, sitebuilds.ModeOn},
		"named on single-host":        {withMode(on, "docker"), sitebuilds.ModeSingleHost},
		"single-host with its flag":   {withSingleHostBuilds(withMode(on, "docker")), sitebuilds.ModeOn},
		"the flag alone in a cluster": {withSingleHostBuilds(on), sitebuilds.ModeOn},
		"the flag without git builds": {withSingleHostBuilds(withMode(config.AppConfig{}, "docker")), sitebuilds.ModeOff},
	}
	for name, tc := range cases {
		if got := siteBuildMode(tc.cfg); got != tc.want {
			t.Errorf("%s: %v, want %v", name, got, tc.want)
		}
	}
}

func withSingleHostBuilds(cfg config.AppConfig) config.AppConfig {
	cfg.DarkFeatures = append(cfg.DarkFeatures, features.SingleHostBuilds)
	return cfg
}

// A single host that cannot run builds refuses to start, as a cluster without the RuntimeClass does.
func TestSingleHostBuildRunnerRefusesWithoutAWorkingEngine(t *testing.T) {
	cfg := withSingleHostBuilds(config.AppConfig{ProvisionerMode: "docker"})
	cfg.SiteGit.EgressImage, cfg.SiteGit.PrepareImage = "docker.io/excalibase/provisioning:ci", config.DefaultSiteBuildPrepareImage
	cfg.SingleHostApps.NetworkPrefix, cfg.SingleHostApps.VolumePrefix, cfg.SingleHostApps.SandboxRuntime = "excalibase-net-", "excalibase-vol-", "runsc"
	if _, err := singleHostBuildRunner(context.Background(), cfg, nil); err == nil || !strings.Contains(err.Error(), "engine") {
		t.Fatalf("no engine: %v", err)
	}
	unreachable, err := client.NewClientWithOpts(client.WithHost("tcp://127.0.0.1:1"), client.WithAPIVersionNegotiation())
	if err != nil {
		t.Fatalf("client: %v", err)
	}
	if _, err := singleHostBuildRunner(context.Background(), cfg, rawClient{unreachable}); err == nil {
		t.Fatal("an engine that does not answer was accepted")
	}
}

type rawClient struct{ sdk *client.Client }

func (r rawClient) RawClient() *client.Client { return r.sdk }

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

// A single host pulls the Node image inside Start, so its start window is longer than a cluster's.
func TestSiteBuildDispatcherOptionsCoverAPullOnASingleHost(t *testing.T) {
	on := config.AppConfig{SitesEnabled: true, SitesGitBuildsEnabled: true}
	if got := siteBuildDispatcherOptions(on).StartTimeout; got != 0 {
		t.Fatalf("cluster start window %v, want the dispatcher's default", got)
	}
	if got := siteBuildDispatcherOptions(withSingleHostBuilds(withMode(on, "docker"))).StartTimeout; got != singleHostBuildStartTimeout {
		t.Fatalf("single-host start window %v", got)
	}
}
