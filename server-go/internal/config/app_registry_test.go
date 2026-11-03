package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/features"
)

func TestLoadAppRegistry_ReadsTheHostAndTheKeyFile(t *testing.T) {
	key := filepath.Join(t.TempDir(), "token.key")
	if err := os.WriteFile(key, []byte("-----BEGIN PRIVATE KEY-----\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("APP_REGISTRY_HOST", " registry.excalibase.internal ")
	t.Setenv("APP_REGISTRY_TOKEN_KEY_FILE", key)
	t.Setenv("APP_REGISTRY_ALIASES", " Zot.Excalibase.svc.cluster.local, 10.43.200.10 ,")
	t.Setenv("APP_REGISTRY_URL", " http://app-registry.ns.svc:5000 ")
	registry := loadAppRegistry()
	if len(registry.Aliases) != 2 || registry.Aliases[0] != "zot.excalibase.svc.cluster.local" || registry.URL != "http://app-registry.ns.svc:5000" {
		t.Errorf("aliases %v", registry.Aliases)
	}
	if registry.Host != "registry.excalibase.internal" || !strings.HasPrefix(string(registry.TokenKeyPEM), "-----BEGIN") {
		t.Fatalf("%+v", registry)
	}
	if err := (AppConfig{AppRegistry: registry}).validateAppRegistry(); err != nil {
		t.Fatal(err)
	}
}

func TestValidateAppRegistry(t *testing.T) {
	t.Setenv("APP_REGISTRY_HOST", "registry.excalibase.internal")
	t.Setenv("APP_REGISTRY_TOKEN_KEY_FILE", filepath.Join(t.TempDir(), "missing"))
	missing := loadAppRegistry()
	on := []features.Feature{features.AppGitBuilds}
	cases := map[string]AppConfig{
		"no key file, builds on": {DarkFeatures: on, AppRegistry: AppRegistryConfig{Host: "registry.excalibase.internal", URL: "http://zot:5000"}},
		"no URL, builds on":      {DarkFeatures: on, AppRegistry: AppRegistryConfig{Host: "registry.excalibase.internal", TokenKeyPEM: []byte("k")}},
		"unreadable key":         {AppRegistry: missing},
		"single host":            {ProvisionerMode: "docker", AppRegistry: AppRegistryConfig{Host: "r.internal", TokenKeyPEM: []byte("k")}},
		"docker hub":             {AppRegistry: AppRegistryConfig{Host: "docker.io", TokenKeyPEM: []byte("k")}},
		"an operator's one":      {AppLocalRegistries: []string{"r.internal"}, AppRegistry: AppRegistryConfig{Host: "r.internal", TokenKeyPEM: []byte("k")}},
		"docker hub alias":       {AppRegistry: AppRegistryConfig{Host: "r.internal", Aliases: []string{"docker.io"}, TokenKeyPEM: []byte("k")}},
		"an operator's alias":    {AppLocalRegistries: []string{"zot.local"}, AppRegistry: AppRegistryConfig{Host: "r.internal", Aliases: []string{"zot.local"}, TokenKeyPEM: []byte("k")}},
		"a malformed alias":      {AppRegistry: AppRegistryConfig{Host: "r.internal", Aliases: []string{"http://zot"}, TokenKeyPEM: []byte("k")}},
	}
	for name, cfg := range cases {
		if err := cfg.validateAppRegistry(); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if err := (AppConfig{}).validateAppRegistry(); err != nil {
		t.Errorf("no registry is valid: %v", err)
	}
	if err := (AppConfig{AppRegistry: AppRegistryConfig{Host: "registry.excalibase.internal"}}).validateAppRegistry(); err != nil {
		t.Errorf("the host alone guards user images with builds off: %v", err)
	}
	full := AppConfig{DarkFeatures: on, AppRegistry: AppRegistryConfig{Host: "registry.excalibase.internal", URL: "http://zot:5000", TokenKeyPEM: []byte("k")}}
	if err := full.validateAppRegistry(); err != nil {
		t.Errorf("builds on with a key and a URL: %v", err)
	}
}

// Git builds push to and pull from the platform's registry: the flag with no host must stop the boot, not start half-wired.
func TestValidateAppRegistry_GitBuildsNeedAHost(t *testing.T) {
	on := []features.Feature{features.AppGitBuilds}
	err := (AppConfig{DarkFeatures: on}).validateAppRegistry()
	if err == nil || !strings.Contains(err.Error(), "APP_REGISTRY_HOST") || !strings.Contains(err.Error(), "FEATURE_APPGITBUILDS") {
		t.Fatalf("err = %v", err)
	}
	if err := (AppConfig{DarkFeatures: on, AppRegistry: AppRegistryConfig{Host: "r.internal", URL: "http://zot:5000", TokenKeyPEM: []byte("k")}}).validateAppRegistry(); err != nil {
		t.Errorf("host, key and URL with builds on: %v", err)
	}
}

// Single-host shows git-build plan limits only (EXC-677 builds come later), so the flag boots there with no registry.
func TestValidateAppRegistry_SingleHostGitBuildsBootWithoutAHost(t *testing.T) {
	on := []features.Feature{features.AppGitBuilds}
	if err := (AppConfig{ProvisionerMode: "docker", DarkFeatures: on}).validateAppRegistry(); err != nil {
		t.Errorf("single host with builds on and no host: %v", err)
	}
	err := (AppConfig{ProvisionerMode: "k8s", DarkFeatures: on}).validateAppRegistry()
	if err == nil || !strings.Contains(err.Error(), "APP_REGISTRY_HOST") {
		t.Errorf("cluster with builds on and no host: err = %v", err)
	}
}
