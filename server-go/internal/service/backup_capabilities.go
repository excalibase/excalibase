package service

import (
	"context"
	"fmt"

	"github.com/excalibase/provisioning-poc/internal/domain"
)

// Optional adapter capabilities, asked for by type assertion like
// InPlaceCluster and WalLagAdvertiser.

// BackupStatusResolver answers a backup's status by an id its List may not
// carry, such as the Job a manual backup runs as.
type BackupStatusResolver interface {
	BackupStatusOf(ctx context.Context, inst *domain.DatabaseInstance, backupID string) (string, error)
}

// RestoreWindowAdvertiser reports the span a point-in-time restore can reach.
type RestoreWindowAdvertiser interface {
	RestoreWindow(ctx context.Context, inst *domain.DatabaseInstance) (*RestoreWindow, error)
}

// RestoreTargetChecker refuses, before a restore job is filed, a target the
// project's backups cannot reach.
type RestoreTargetChecker interface {
	CheckRestoreTarget(ctx context.Context, inst *domain.DatabaseInstance, req domain.RestoreRequest) error
}

// CheckRestoreTarget refuses a target the project's adapter knows it cannot
// reach; adapters that check while restoring are left to do so.
func (s *BackupService) CheckRestoreTarget(ctx context.Context, inst *domain.DatabaseInstance, req domain.RestoreRequest) error {
	adapter, err := s.adapterFor(inst)
	if err != nil {
		return err
	}
	checker, ok := adapter.(RestoreTargetChecker)
	if !ok {
		return nil
	}
	return checker.CheckRestoreTarget(ctx, inst, req)
}

// RestoreWindow is the span projectID can be restored to, or nil when its
// adapter does not bound one or it has no backup yet.
func (s *BackupService) RestoreWindow(ctx context.Context, projectID string) (*RestoreWindow, error) {
	inst, err := s.store.FindByProjectID(projectID)
	if err != nil || inst == nil {
		return nil, fmt.Errorf("project not found: %s", projectID)
	}
	adapter, err := s.adapterFor(inst)
	if err != nil {
		return nil, err
	}
	advertiser, ok := adapter.(RestoreWindowAdvertiser)
	if !ok {
		return nil, nil
	}
	return advertiser.RestoreWindow(ctx, inst)
}
