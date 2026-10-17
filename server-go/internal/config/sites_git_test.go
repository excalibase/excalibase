package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/domain"
)

// fakePEM is shaped like a key file without being one, built so secret scanners do not flag it.
func fakePEM(body string) string {
	kind := "PRIVATE " + "KEY"
	return "-----BEGIN " + kind + "-----\n" + body + "\n-----END " + kind + "-----"
}

func setSiteGitEnv(t *testing.T) {
	t.Helper()
	setSitesEnv(t)
	t.Setenv("SITES_GIT_BUILDS_ENABLED", "true")
	t.Setenv("SITES_GITHUB_APP_ID", "12345")
	t.Setenv("SITES_GITHUB_APP_SLUG", "excalibase-sites")
	t.Setenv("SITES_GITHUB_CLIENT_ID", "Iv1.client")
	t.Setenv("SITES_GITHUB_CLIENT_SECRET", "client-secret-value")
	t.Setenv("SITES_GITHUB_APP_PRIVATE_KEY", fakePEM("key"))
	t.Setenv("SITES_GITHUB_WEBHOOK_SECRET", "webhook-secret-value")
	t.Setenv("SITES_BUILD_PRIORITY_CLASS", "site-build-low")
}

func TestSiteGit_ReadsItsSettings(t *testing.T) {
	setSiteGitEnv(t)
	t.Setenv("SITES_BUILD_SLOTS", "3")
	t.Setenv("SITES_BUILD_NAMESPACE", "site-builds")
	t.Setenv("SITES_BUILD_PRIORITY_CLASS", "site-build-low")
	cfg := Load()
	git := cfg.SiteGit
	if !cfg.SitesGitBuildsEnabled || git.AppID != 12345 || git.AppSlug != "excalibase-sites" || git.ClientID != "Iv1.client" ||
		git.ClientSecret != "client-secret-value" || git.WebhookSecret != "webhook-secret-value" || !strings.Contains(git.PrivateKeyPEM, "BEGIN") {
		t.Fatalf("git settings: %v", git)
	}
	if git.APIURL != "https://api.github.com" || git.WebURL != "https://github.com" {
		t.Errorf("GitHub endpoints default to github.com, got %s and %s", git.APIURL, git.WebURL)
	}
	if git.BuildSlots != 3 || git.BuildNamespace != "site-builds" || git.PriorityClass != "site-build-low" || git.NodeImage != "" {
		t.Errorf("build settings: %v", git)
	}
	// The prepare step's image is pinned by digest by default, as the Node images are.
	if !strings.HasPrefix(git.PrepareImage, "busybox:") || !strings.Contains(git.PrepareImage, "@sha256:") {
		t.Errorf("prepare image %q", git.PrepareImage)
	}
	if !git.Complete() {
		t.Error("every GitHub App setting was given")
	}
	if err := cfg.validateSiteGit(); err != nil {
		t.Errorf("valid settings refused: %v", err)
	}
}

func TestSiteGit_OffByDefault(t *testing.T) {
	setSitesEnv(t)
	cfg := Load()
	if cfg.SitesGitBuildsEnabled {
		t.Fatal("git builds must be off unless SITES_GIT_BUILDS_ENABLED=true")
	}
	if cfg.SiteGit.BuildSlots != 1 || cfg.SiteGit.BuildNamespace != "excalibase-site-builds" {
		t.Errorf("defaults: %v", cfg.SiteGit)
	}
}

func TestSiteGit_PrivateKeyFromAFile(t *testing.T) {
	setSiteGitEnv(t)
	path := filepath.Join(t.TempDir(), "key.pem")
	if err := os.WriteFile(path, []byte(fakePEM("from-file")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SITES_GITHUB_APP_PRIVATE_KEY", "")
	t.Setenv("SITES_GITHUB_APP_PRIVATE_KEY_FILE", path)
	cfg := Load()
	if !strings.Contains(cfg.SiteGit.PrivateKeyPEM, "from-file") {
		t.Fatalf("key file not read: %q", cfg.SiteGit.PrivateKeyPEM)
	}
}

func TestSiteGit_RefusesBadSettings(t *testing.T) {
	cases := map[string]func(*AppConfig){
		"unreadable key file": func(c *AppConfig) { c.SiteGit.keyFileErr = os.ErrNotExist },
		"bad app id":          func(c *AppConfig) { c.SiteGit.appIDErr = true },
		"no build slots":      func(c *AppConfig) { c.SiteGit.BuildSlots = 0 },
		"bad namespace":       func(c *AppConfig) { c.SiteGit.BuildNamespace = "Not_A_Namespace" },
		"http api url":        func(c *AppConfig) { c.SiteGit.APIURL = "ftp://api.github.com" },
		// Builds must yield to tenants' workloads when a node runs short.
		"no priority class in a cluster": func(c *AppConfig) { c.ProvisionerMode, c.SiteGit.PriorityClass = "k8s", "" },
		"no prepare image":               func(c *AppConfig) { c.SiteGit.PrepareImage = "" },
	}
	for name, breakIt := range cases {
		t.Run(name, func(t *testing.T) {
			setSiteGitEnv(t)
			cfg := Load()
			breakIt(&cfg)
			if err := cfg.validateSiteGit(); err == nil {
				t.Fatal("want a refusal")
			}
		})
	}
}

func TestSiteGit_SecretsNeverFormatted(t *testing.T) {
	git := SiteGitConfig{ClientSecret: "client-secret-value", WebhookSecret: "webhook-secret-value", PrivateKeyPEM: "PEM-BODY"}
	for _, verb := range []string{"%v", "%+v", "%s", "%#v"} {
		out := sprintf(verb, git)
		for _, secret := range []string{"client-secret-value", "webhook-secret-value", "PEM-BODY"} {
			if strings.Contains(out, secret) {
				t.Errorf("%s prints a secret: %s", verb, out)
			}
		}
	}
}

func TestTiersCarryTheirSiteBuildLimits(t *testing.T) {
	want := map[domain.TierType]SiteBuildTierLimits{
		domain.Free:       {CPUMillis: 2000, Memory: "2Gi", Disk: "5Gi", TimeoutSeconds: 600, Concurrency: 1},
		domain.Standard:   {CPUMillis: 2000, Memory: "4Gi", Disk: "10Gi", TimeoutSeconds: 1200, Concurrency: 2},
		domain.Enterprise: {CPUMillis: 2000, Memory: "4Gi", Disk: "10Gi", TimeoutSeconds: 1200, Concurrency: 2},
	}
	for tier, limits := range want {
		tc, err := GetTierConfig(tier)
		if err != nil {
			t.Fatalf("%s: %v", tier, err)
		}
		if tc.SiteBuilds != limits {
			t.Errorf("%s: build limits %+v, want %+v", tier, tc.SiteBuilds, limits)
		}
	}
}

// Single-host refuses git builds by name, so it needs no priority class.
func TestSiteGit_SingleHostNeedsNoPriorityClass(t *testing.T) {
	setSiteGitEnv(t)
	cfg := Load()
	cfg.ProvisionerMode, cfg.SiteGit.PriorityClass = "docker", ""
	if err := cfg.validateSiteGit(); err != nil {
		t.Fatal(err)
	}
}
