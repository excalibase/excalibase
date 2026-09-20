package service

import (
	"context"
	"errors"
	"fmt"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/storage"
)

// OrgProjectLimitError is the single refusal a caller is given when their
// organisation has no free project slot. It names the limit and the tier —
// what the caller can act on — and never the organisation id.
type OrgProjectLimitError struct {
	Limit int
	Tier  domain.TierType
}

func (e *OrgProjectLimitError) Error() string {
	return fmt.Sprintf("organization has reached its project limit of %d for the %s tier", e.Limit, e.Tier)
}

// Unwrap lets callers recognise the refusal through the store's sentinel
// without depending on which store answered.
func (e *OrgProjectLimitError) Unwrap() error { return storage.ErrOrgProjectLimitReached }

// ErrProjectStoreUnavailable is returned when the platform database could not
// say whether the organisation has room. The request fails: an unanswered
// count is never read as free capacity.
var ErrProjectStoreUnavailable = errors.New("the project could not be registered")

// OrgProjectCapacity answers whether an organisation may take one more
// project. *ProvisioningService implements it; the backup service depends on
// this narrow surface so a restore is metered exactly like a provision.
type OrgProjectCapacity interface {
	EnsureOrgProjectCapacity(ctx context.Context, orgID string, tier domain.TierType) error
}

// EnsureOrgProjectCapacity refuses a new project before anything is created
// when the organisation is already at its tier limit. It is an early answer,
// not the enforcement: the slot is taken atomically at insert time, so two
// callers that pass this check at once still cannot both get the last slot.
func (s *ProvisioningService) EnsureOrgProjectCapacity(ctx context.Context, orgID string, tierType domain.TierType) error {
	limit, err := s.orgProjectLimit(ctx, tierType)
	if err != nil {
		return err
	}
	if limit <= 0 {
		return nil
	}
	held, err := s.store.CountOrgProjects(orgID)
	if err != nil {
		return fmt.Errorf("%w: count org projects: %v", ErrProjectStoreUnavailable, err)
	}
	if storage.CheckOrgProjectSlot(held, limit) != nil {
		return &OrgProjectLimitError{Limit: limit, Tier: tierType}
	}
	return nil
}

// orgProjectLimit resolves how many projects the tier allows an organisation.
// Zero or less means unlimited, which is what self-hosted installs always get:
// they are not metered.
func (s *ProvisioningService) orgProjectLimit(ctx context.Context, tierType domain.TierType) (int, error) {
	if s.selfHostedMode {
		return 0, nil
	}
	tier, err := s.tierConfig(ctx, tierType)
	if err != nil {
		return 0, err
	}
	return tier.MaxProjects, nil
}

// createProjectRow registers the project row and takes its organisation's
// slot in the same step. Every path that brings a project into existence —
// provision and restore alike — goes through here, so the limit cannot be
// enforced on one and forgotten on the other.
func (s *ProvisioningService) createProjectRow(ctx context.Context, inst *domain.DatabaseInstance, tierType domain.TierType) error {
	limit, err := s.orgProjectLimit(ctx, tierType)
	if err != nil {
		return err
	}
	switch err := s.store.CreateWithinOrgLimit(inst, limit); {
	case err == nil:
		return nil
	case errors.Is(err, storage.ErrOrgProjectLimitReached):
		return &OrgProjectLimitError{Limit: limit, Tier: tierType}
	case errors.Is(err, storage.ErrProjectExists):
		return err
	default:
		return fmt.Errorf("%w: create instance: %v", ErrProjectStoreUnavailable, err)
	}
}
