package config

import (
	"errors"
	"slices"
	"testing"
)

func TestStorageClassPolicyEmptyRequestTakesTheDefault(t *testing.T) {
	policy := StorageClassPolicy{Default: "standard-rwo", Allowed: []string{"fast-ssd"}}
	got, err := policy.Resolve("")
	if err != nil || got != "standard-rwo" {
		t.Fatalf("Resolve(\"\") = %q, %v; want the platform default", got, err)
	}
}

func TestStorageClassPolicyAdmitsTheDefaultAndTheAllowlist(t *testing.T) {
	policy := StorageClassPolicy{Default: "standard-rwo", Allowed: []string{"fast-ssd"}}
	for _, requested := range []string{"standard-rwo", "fast-ssd"} {
		if got, err := policy.Resolve(requested); err != nil || got != requested {
			t.Errorf("Resolve(%q) = %q, %v; want it admitted", requested, got, err)
		}
	}
}

func TestStorageClassPolicyRefusesAnythingElse(t *testing.T) {
	cases := map[string]StorageClassPolicy{
		"not on the allowlist":        {Default: "standard-rwo", Allowed: []string{"fast-ssd"}},
		"no allowlist, named default": {Default: "standard-rwo"},
		"cluster default only":        {},
	}
	for name, policy := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := policy.Resolve("local-path"); !errors.Is(err, ErrStorageClassNotAllowed) {
				t.Fatalf("err = %v, want ErrStorageClassNotAllowed", err)
			}
		})
	}
}

func TestStorageClassPolicyWithNothingConfiguredUsesTheClusterDefault(t *testing.T) {
	got, err := StorageClassPolicy{}.Resolve("")
	if err != nil || got != "" {
		t.Fatalf("Resolve(\"\") = %q, %v; want the cluster's own default (empty)", got, err)
	}
}

func TestLoadReadsTheTenantStorageClasses(t *testing.T) {
	t.Setenv("CORS_ORIGINS", "https://app.example.com")
	t.Setenv("TENANT_STORAGE_CLASS", "standard-rwo")
	t.Setenv("TENANT_STORAGE_CLASSES", " fast-ssd , ,premium ")
	policy := Load().TenantStorageClassPolicy()
	if policy.Default != "standard-rwo" || !slices.Equal(policy.Allowed, []string{"fast-ssd", "premium"}) {
		t.Errorf("policy = %+v", policy)
	}
}

func TestValidateRefusesAStorageClassThatIsNotAName(t *testing.T) {
	cases := map[string]AppConfig{
		"default":   {ProvisionerMode: "k8s", TenantStorageClass: "Not A Class"},
		"allowlist": {ProvisionerMode: "k8s", TenantStorageClasses: []string{"ok", "bad/class"}},
	}
	for name, cfg := range cases {
		t.Run(name, func(t *testing.T) {
			if err := cfg.Validate(); err == nil {
				t.Error("an invalid storage class name must be refused at boot")
			}
		})
	}
}
