package config

import (
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"

	"k8s.io/apimachinery/pkg/util/validation"
)

// ErrStorageClassNotAllowed refuses a storage class the platform does not offer tenants.
var ErrStorageClassNotAllowed = errors.New("storage class is not offered for projects")

// StorageClassPolicy is which storage classes a tenant's database may run on.
// Default is used when a request names none; empty means the cluster's own
// default StorageClass. Allowed are the further classes a request may name.
type StorageClassPolicy struct {
	Default string
	Allowed []string
}

// Resolve returns the storage class a request for requested runs on.
func (p StorageClassPolicy) Resolve(requested string) (string, error) {
	if requested == "" {
		return p.Default, nil
	}
	if requested == p.Default || slices.Contains(p.Allowed, requested) {
		return requested, nil
	}
	return "", fmt.Errorf("%w: %q", ErrStorageClassNotAllowed, requested)
}

// TenantStorageClassPolicy is the policy TENANT_STORAGE_CLASS and
// TENANT_STORAGE_CLASSES configure.
func (c AppConfig) TenantStorageClassPolicy() StorageClassPolicy {
	return StorageClassPolicy{Default: c.TenantStorageClass, Allowed: slices.Clone(c.TenantStorageClasses)}
}

func (c AppConfig) validateTenantStorageClasses() error {
	names := c.TenantStorageClasses
	if c.TenantStorageClass != "" {
		names = append([]string{c.TenantStorageClass}, names...)
	}
	for _, name := range names {
		if problems := validation.IsDNS1123Subdomain(name); len(problems) > 0 {
			return fmt.Errorf("tenant storage class %q is not a StorageClass name: %s", name, strings.Join(problems, "; "))
		}
	}
	return nil
}

func envList(key string) []string {
	var values []string
	for _, part := range strings.Split(os.Getenv(key), ",") {
		if value := strings.TrimSpace(part); value != "" {
			values = append(values, value)
		}
	}
	return values
}
