package config

import (
	"slices"
	"strings"
	"testing"
)

const pinnedTools = "busybox:1.37.0@sha256:bdf57e528e45e4433820e045b29b4597825a1c9e38353532d90a01445013f82e"

func sizedStorage() AppConfig {
	return AppConfig{
		ProvisionerMode: "k8s", TenantStorageClass: "excalibase-tenant",
		TenantStorageRequireSized: true, TenantSizedProvisioners: []string{"local.csi.openebs.io"},
		AppDiskToolsImage: pinnedTools, StorageBudgetPercent: 80,
		StorageLVMNamespace: "openebs", StorageLVMVolumeGroup: "excalibase-tenants",
	}
}

func TestLoadReadsTheTenantStorageSettings(t *testing.T) {
	t.Setenv("CORS_ORIGINS", "https://app.example.com")
	t.Setenv("TENANT_STORAGE_REQUIRE_SIZED", "true")
	t.Setenv("TENANT_SIZED_PROVISIONERS", "local.csi.openebs.io, topolvm.io")
	t.Setenv("APP_DISK_TOOLS_IMAGE", pinnedTools)
	t.Setenv("STORAGE_BUDGET_PERCENT", "85")
	t.Setenv("STORAGE_NODE_CAPACITY", "2Ti")
	t.Setenv("STORAGE_LVM_VOLUME_GROUP", "excalibase-tenants")
	cfg := Load()
	if !cfg.TenantStorageRequireSized || !slices.Equal(cfg.TenantSizedProvisioners, []string{"local.csi.openebs.io", "topolvm.io"}) {
		t.Errorf("sized = %v %v", cfg.TenantStorageRequireSized, cfg.TenantSizedProvisioners)
	}
	if cfg.AppDiskToolsImage != pinnedTools || cfg.StorageBudgetPercent != 85 || cfg.StorageNodeCapacity != "2Ti" {
		t.Errorf("cfg = %q %d %q", cfg.AppDiskToolsImage, cfg.StorageBudgetPercent, cfg.StorageNodeCapacity)
	}
	if cfg.StorageLVMNamespace != "openebs" || cfg.StorageLVMVolumeGroup != "excalibase-tenants" {
		t.Errorf("lvm = %q %q", cfg.StorageLVMNamespace, cfg.StorageLVMVolumeGroup)
	}
}

func TestLoadTenantStorageDefaults(t *testing.T) {
	t.Setenv("CORS_ORIGINS", "https://app.example.com")
	cfg := Load()
	if cfg.TenantStorageRequireSized || cfg.StorageBudgetPercent != 80 || cfg.StorageLVMNamespace != "openebs" {
		t.Errorf("defaults = %v %d %q", cfg.TenantStorageRequireSized, cfg.StorageBudgetPercent, cfg.StorageLVMNamespace)
	}
	if !slices.Equal(cfg.TenantSizedProvisioners, []string{"local.csi.openebs.io"}) {
		t.Errorf("sized provisioners = %v", cfg.TenantSizedProvisioners)
	}
}

func TestValidateSizedTenantStorage(t *testing.T) {
	if err := sizedStorage().Validate(); err != nil {
		t.Fatalf("a complete sized setup refused: %v", err)
	}
	withFixedCapacity := sizedStorage()
	withFixedCapacity.StorageLVMVolumeGroup, withFixedCapacity.StorageNodeCapacity = "", "2Ti"
	if err := withFixedCapacity.Validate(); err != nil {
		t.Fatalf("a fixed node capacity refused: %v", err)
	}
	cases := map[string]struct {
		change func(*AppConfig)
		want   string
	}{
		"no class":                 {func(c *AppConfig) { c.TenantStorageClass = "" }, "TENANT_STORAGE_CLASS"},
		"no sized provisioner":     {func(c *AppConfig) { c.TenantSizedProvisioners = nil }, "TENANT_SIZED_PROVISIONERS"},
		"tools image by tag only":  {func(c *AppConfig) { c.AppDiskToolsImage = "busybox:latest" }, "APP_DISK_TOOLS_IMAGE"},
		"no capacity source":       {func(c *AppConfig) { c.StorageLVMVolumeGroup = "" }, "STORAGE_NODE_CAPACITY"},
		"budget share of zero":     {func(c *AppConfig) { c.StorageBudgetPercent = 0 }, "STORAGE_BUDGET_PERCENT"},
		"budget share of the disk": {func(c *AppConfig) { c.StorageBudgetPercent = 100 }, "STORAGE_BUDGET_PERCENT"},
		"a capacity that is not a size": {func(c *AppConfig) {
			c.StorageLVMVolumeGroup, c.StorageNodeCapacity = "", "two terabytes"
		}, "STORAGE_NODE_CAPACITY"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			cfg := sizedStorage()
			tc.change(&cfg)
			err := cfg.Validate()
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want a refusal naming %s", err, tc.want)
			}
		})
	}
}

// A test cluster on local-path runs without sized volumes, and without a
// capacity the budget is simply not enforced there.
func TestValidateUnsizedTenantStorageNeedsNothing(t *testing.T) {
	cfg := AppConfig{ProvisionerMode: "k8s", StorageBudgetPercent: 80}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("unsized: %v", err)
	}
}
