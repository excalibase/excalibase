package config

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"k8s.io/apimachinery/pkg/api/resource"
)

// Tenant disks and the platform's storage budget (EXC-523, EXC-492).
//
// TENANT_STORAGE_REQUIRE_SIZED is the production switch: tenant disks must be
// on a class whose volumes are really the size they claim, and the platform
// must know the storage it allocates from, or provisioning does not start.

const (
	defaultStorageBudgetPercent = 80
	maxStorageBudgetPercent     = 95
	defaultLVMNamespace         = "openebs"
	defaultSizedProvisioner     = "local.csi.openebs.io"
)

var errTenantStorage = errors.New("tenant storage")

func loadTenantStorage(c *AppConfig) {
	c.TenantStorageRequireSized = envBool("TENANT_STORAGE_REQUIRE_SIZED", false)
	c.TenantSizedProvisioners = envList("TENANT_SIZED_PROVISIONERS")
	if len(c.TenantSizedProvisioners) == 0 {
		c.TenantSizedProvisioners = []string{defaultSizedProvisioner}
	}
	c.AppDiskToolsImage = strings.TrimSpace(os.Getenv("APP_DISK_TOOLS_IMAGE"))
	c.StorageBudgetPercent = envInt("STORAGE_BUDGET_PERCENT", defaultStorageBudgetPercent)
	c.StorageNodeCapacity = strings.TrimSpace(os.Getenv("STORAGE_NODE_CAPACITY"))
	c.StorageLVMNamespace = envOr("STORAGE_LVM_NAMESPACE", defaultLVMNamespace)
	c.StorageLVMVolumeGroup = strings.TrimSpace(os.Getenv("STORAGE_LVM_VOLUME_GROUP"))
}

// StorageBudgetEnabled reports whether the platform knows the storage it
// allocates from, so every allocation is held to the budget.
func (c AppConfig) StorageBudgetEnabled() bool {
	return c.StorageLVMVolumeGroup != "" || c.StorageNodeCapacity != ""
}

// StorageNodeCapacityBytes is STORAGE_NODE_CAPACITY in bytes; 0 when unset.
func (c AppConfig) StorageNodeCapacityBytes() (int64, error) {
	if c.StorageNodeCapacity == "" {
		return 0, nil
	}
	quantity, err := resource.ParseQuantity(c.StorageNodeCapacity)
	if err != nil || quantity.Sign() <= 0 {
		return 0, fmt.Errorf("%w: STORAGE_NODE_CAPACITY %q is not a size such as 2Ti", errTenantStorage, c.StorageNodeCapacity)
	}
	return quantity.Value(), nil
}

func (c AppConfig) validateTenantStorage() error {
	if c.StorageBudgetEnabled() || c.TenantStorageRequireSized {
		if c.StorageBudgetPercent < 1 || c.StorageBudgetPercent > maxStorageBudgetPercent {
			return fmt.Errorf("%w: STORAGE_BUDGET_PERCENT must be 1 to %d, got %d", errTenantStorage, maxStorageBudgetPercent, c.StorageBudgetPercent)
		}
		if _, err := c.StorageNodeCapacityBytes(); err != nil {
			return err
		}
	}
	if !c.TenantStorageRequireSized {
		return nil
	}
	switch {
	case c.TenantStorageClass == "":
		return fmt.Errorf("%w: TENANT_STORAGE_REQUIRE_SIZED needs TENANT_STORAGE_CLASS, the class tenant disks are made on", errTenantStorage)
	case len(c.TenantSizedProvisioners) == 0:
		return fmt.Errorf("%w: TENANT_STORAGE_REQUIRE_SIZED needs TENANT_SIZED_PROVISIONERS", errTenantStorage)
	case !strings.Contains(c.AppDiskToolsImage, "@sha256:"):
		return fmt.Errorf("%w: APP_DISK_TOOLS_IMAGE must be pinned by digest, got %q", errTenantStorage, c.AppDiskToolsImage)
	case !c.StorageBudgetEnabled():
		return fmt.Errorf("%w: TENANT_STORAGE_REQUIRE_SIZED needs the storage the budget is taken from: STORAGE_LVM_VOLUME_GROUP, or STORAGE_NODE_CAPACITY", errTenantStorage)
	}
	return nil
}
