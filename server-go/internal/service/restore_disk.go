package service

import (
	"context"
	"errors"
	"fmt"

	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"k8s.io/apimachinery/pkg/api/resource"
)

var (
	// ErrRestoreDiskAbovePlan refuses a restore whose source disk is larger
	// than the target plan allows: the data may not fit, and a disk cannot be
	// shrunk to make it.
	ErrRestoreDiskAbovePlan = errors.New("the source's disk is larger than the organization's plan allows")
	// ErrRestoreSourceDiskUnknown refuses a restore from a project with no
	// recorded disk: it may have grown, so the plan's start is not a size.
	ErrRestoreSourceDiskUnknown = errors.New("the source project records no disk size")
)

// RequireRestoreDiskFits refuses, before anything is created, a restore whose
// source disk the organisation's current plan cannot hold.
func (s *ProvisioningService) RequireRestoreDiskFits(ctx context.Context, source *domain.DatabaseInstance) error {
	tierType, err := s.orgTier(ctx, source.OrgID)
	if err != nil {
		return err
	}
	tier, err := s.tierConfig(ctx, tierType)
	if err != nil {
		return err
	}
	disk, err := restoreDisk(source, tier)
	if err != nil {
		return err
	}
	return s.RequireStorageForDatabase(ctx, tier.Instances, disk)
}

// restoreDisk is the disk a project restored from source gets on tier: the
// source's recorded disk, never below the plan's start.
func restoreDisk(source *domain.DatabaseInstance, tier config.TierConfig) (string, error) {
	recorded, err := resource.ParseQuantity(source.StorageSize)
	if source.StorageSize == "" || err != nil {
		return "", fmt.Errorf("%w: %s", ErrRestoreSourceDiskUnknown, source.ProjectID)
	}
	start, err := resource.ParseQuantity(tier.StorageSize)
	if err != nil {
		return "", fmt.Errorf("plan disk %q: %w", tier.StorageSize, err)
	}
	limit, err := resource.ParseQuantity(tier.MaxStorageSize)
	if err != nil {
		return "", fmt.Errorf("plan maximum disk %q: %w", tier.MaxStorageSize, err)
	}
	if recorded.Cmp(limit) > 0 {
		return "", fmt.Errorf("%w: the plan allows up to %s and the source's disk is %s", ErrRestoreDiskAbovePlan, tier.MaxStorageSize, source.StorageSize)
	}
	if recorded.Cmp(start) < 0 {
		return tier.StorageSize, nil
	}
	return source.StorageSize, nil
}
