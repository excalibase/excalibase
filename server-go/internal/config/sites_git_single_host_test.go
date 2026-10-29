package config

import (
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/features"
)

func singleHostBuildsConfig() AppConfig {
	return AppConfig{
		ProvisionerMode: "docker", SitesGitBuildsEnabled: true, DarkFeatures: []features.Feature{features.SingleHostBuilds},
		SiteGit: SiteGitConfig{BuildSlots: 1, PrepareImage: DefaultSiteBuildPrepareImage, BuildNamespace: "excalibase-site-builds",
			APIURL: "https://api.github.com", WebURL: "https://github.com", EgressImage: "docker.io/excalibase/provisioning:1.6.0"},
	}
}

func TestLoadSiteGitReadsTheSingleHostSettings(t *testing.T) {
	t.Setenv("SITES_BUILD_EGRESS_IMAGE", " docker.io/excalibase/provisioning:ci ")
	t.Setenv("SITES_BUILD_EGRESS_ALLOW", "git.lan:8080, registry.lan:443")
	git := loadSiteGit()
	if git.EgressImage != "docker.io/excalibase/provisioning:ci" || strings.Join(git.EgressAllow, "|") != "git.lan:8080|registry.lan:443" {
		t.Fatalf("loaded %+v", git)
	}
}

func TestSingleHostBuildsOnNeedsTheFlagAndDockerMode(t *testing.T) {
	cfg := singleHostBuildsConfig()
	if !cfg.SingleHostSiteBuilds() {
		t.Fatal("docker mode with the flag is off")
	}
	cfg.DarkFeatures = nil
	if cfg.SingleHostSiteBuilds() {
		t.Fatal("on without the flag")
	}
	cfg = singleHostBuildsConfig()
	cfg.ProvisionerMode = "k8s"
	if cfg.SingleHostSiteBuilds() {
		t.Fatal("on in a cluster")
	}
}

func TestValidateSiteGitOnASingleHost(t *testing.T) {
	if err := singleHostBuildsConfig().validateSiteGit(); err != nil {
		t.Fatalf("valid settings refused: %v", err)
	}
	cfg := singleHostBuildsConfig()
	cfg.SiteGit.EgressImage = ""
	if err := cfg.validateSiteGit(); err == nil || !strings.Contains(err.Error(), "SITES_BUILD_EGRESS_IMAGE") {
		t.Fatalf("no egress image: %v", err)
	}
	cfg = singleHostBuildsConfig()
	cfg.SiteGit.EgressAllow = []string{"no-port"}
	if err := cfg.validateSiteGit(); err == nil || !strings.Contains(err.Error(), "SITES_BUILD_EGRESS_ALLOW") {
		t.Fatalf("bad allow entry: %v", err)
	}
	cfg = singleHostBuildsConfig()
	cfg.DarkFeatures, cfg.SiteGit.EgressImage = nil, ""
	if err := cfg.validateSiteGit(); err != nil {
		t.Fatalf("the single-host settings were required with the flag off: %v", err)
	}
}
