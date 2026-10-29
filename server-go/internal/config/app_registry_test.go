package config

import (
	"os"

	"github.com/excalibase/provisioning-poc/internal/features"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadAppRegistry_ReadsTheHostAndTheKeyFile(t *testing.T) {
	key := filepath.Join(t.TempDir(), "token.key")
	if err := os.WriteFile(key, []byte("-----BEGIN PRIVATE KEY-----\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("APP_REGISTRY_HOST", " registry.excalibase.internal ")
	t.Setenv("APP_REGISTRY_TOKEN_KEY_FILE", key)
	t.Setenv("APP_REGISTRY_ALIASES", " Zot.Excalibase.svc.cluster.local, 10.43.200.10 ,")
	registry := loadAppRegistry()
	if len(registry.Aliases) != 2 || registry.Aliases[0] != "zot.excalibase.svc.cluster.local" {
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
		"no key file, builds on": {DarkFeatures: on, AppRegistry: AppRegistryConfig{Host: "registry.excalibase.internal"}},
		"unreadable key":         {AppRegistry: missing},
		"single host":            {ProvisionerMode: "docker", AppRegistry: AppRegistryConfig{Host: "r.internal", TokenKeyPEM: []byte("k")}},
		"docker hub":             {AppRegistry: AppRegistryConfig{Host: "docker.io", TokenKeyPEM: []byte("k")}},
		"an operator's one":      {AppLocalRegistries: []string{"r.internal"}, AppRegistry: AppRegistryConfig{Host: "r.internal", TokenKeyPEM: []byte("k")}},
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
}
