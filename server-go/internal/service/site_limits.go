package service

import (
	"context"
	"fmt"

	"github.com/excalibase/provisioning-poc/internal/sites"
)

var errNoSiteLimits = fmt.Errorf("%w: no source for the plan's site limits", ErrOrgTierUnresolved)

type siteLimits struct {
	plans PlanTiers
	tiers TierConfigSource
}

// NewSiteLimits bounds a project's sites and deploys by its organisation's current plan (EXC-620).
func NewSiteLimits(plans PlanTiers, tiers TierConfigSource) sites.DeployLimiter {
	return siteLimits{plans: plans, tiers: tiers}
}

func (l siteLimits) DeployLimits(ctx context.Context, projectID string) (sites.Limits, error) {
	if l.plans == nil || l.tiers == nil {
		return sites.Limits{}, errNoSiteLimits
	}
	tier, err := l.plans.ProjectPlanTier(ctx, projectID)
	if err != nil {
		return sites.Limits{}, fmt.Errorf("%w: %v", ErrOrgTierUnresolved, err)
	}
	tc, err := l.tiers.TierConfig(ctx, tier)
	if err != nil {
		return sites.Limits{}, fmt.Errorf("%w: %v", ErrOrgTierUnresolved, err)
	}
	plan := tc.Sites
	return sites.Limits{
		MaxFiles: plan.MaxFiles, MaxFileBytes: plan.MaxFileBytes, MaxTotalBytes: plan.MaxDeployBytes,
		MaxSites: plan.MaxSites, MaxDeploysPerHour: plan.MaxDeploysPerHour, MaxCustomDomains: plan.MaxCustomDomains,
		MaxPreviews: plan.MaxPreviews,
	}, nil
}
