package service

import (
	"context"
	"fmt"

	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/excalibase/provisioning-poc/internal/k8s"
	"github.com/excalibase/provisioning-poc/internal/storagebudget"
)

// SetStorageBudget holds every database volume to the platform's storage
// budget. Nil (the default) is an unmetered install.
func (s *ProvisioningService) SetStorageBudget(budget *storagebudget.Budget) {
	s.storageBudget = budget
}

// StorageBudget is the budget database volumes are held to.
func (s *ProvisioningService) StorageBudget() *storagebudget.Budget { return s.storageBudget }

// RequireStorageForDatabase is the early answer for a new database: its
// instances each get a volume of size.
func (s *ProvisioningService) RequireStorageForDatabase(ctx context.Context, instances int, size string) error {
	bytes, err := databaseBytes(instances, size)
	if err != nil {
		return err
	}
	return s.storageBudget.Check(ctx, bytes, fmt.Sprintf("a new database (%d x %s)", instances, size))
}

func databaseBytes(instances int, size string) (int64, error) {
	quantity, err := resource.ParseQuantity(size)
	if err != nil {
		return 0, fmt.Errorf("database disk %q: %w", size, err)
	}
	return int64(instances) * quantity.Value(), nil
}

func clusterBytes(cluster *unstructured.Unstructured) (int64, error) {
	return k8s.ClusterStorageBytes(cluster)
}
