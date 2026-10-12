package config

import (
	"strings"
	"testing"
)

func TestAppLocalRegistries_ReadAsHostsInOneSpelling(t *testing.T) {
	t.Setenv("APP_LOCAL_REGISTRIES", " LocalHost:5000 , registry.lan:5443,,")
	got := Load().AppLocalRegistries
	if strings.Join(got, ",") != "localhost:5000,registry.lan:5443" {
		t.Fatalf("got %q", got)
	}
}

func TestAppLocalRegistries_EmptyByDefault(t *testing.T) {
	if got := Load().AppLocalRegistries; len(got) != 0 {
		t.Fatalf("got %q", got)
	}
}

// A hosted platform must never deploy from a private address on a tenant's
// say-so; the list is for an operator's own host.
func TestValidate_RefusesLocalRegistriesInCloudMode(t *testing.T) {
	cfg := AppConfig{DeploymentMode: "cloud", AppLocalRegistries: []string{"localhost:5000"}}
	if err := cfg.validateAppLocalRegistries(); err == nil || !strings.Contains(err.Error(), "APP_LOCAL_REGISTRIES") {
		t.Fatalf("err = %v", err)
	}
	if err := (AppConfig{DeploymentMode: "selfhosted", AppLocalRegistries: []string{"localhost:5000"}}).validateAppLocalRegistries(); err != nil {
		t.Fatalf("self-hosted: %v", err)
	}
}

func TestValidate_RefusesALocalRegistryThatIsNotAHost(t *testing.T) {
	for _, bad := range []string{"http://localhost:5000", "localhost:5000/team", "localhost:99999", "a b"} {
		cfg := AppConfig{AppLocalRegistries: []string{bad}}
		if err := cfg.validateAppLocalRegistries(); err == nil {
			t.Errorf("%q was accepted", bad)
		}
	}
}

func TestValidate_RunsTheLocalRegistryCheck(t *testing.T) {
	cfg := AppConfig{DeploymentMode: "cloud", AppLocalRegistries: []string{"localhost:5000"}}
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "APP_LOCAL_REGISTRIES") {
		t.Fatalf("err = %v", err)
	}
}
