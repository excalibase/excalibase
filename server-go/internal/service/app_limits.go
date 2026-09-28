package service

import (
	"context"
	"fmt"

	"github.com/excalibase/provisioning-poc/internal/apphost"
)

var errNoAppLimits = fmt.Errorf("%w: no source for the plan's app limit", ErrOrgTierUnresolved)

type appLimits struct {
	plans PlanTiers
	tiers TierConfigSource
}

// NewAppLimits caps a project's app count by its organisation's current plan (EXC-524).
func NewAppLimits(plans PlanTiers, tiers TierConfigSource) apphost.AppLimits {
	return appLimits{plans: plans, tiers: tiers}
}

func (l appLimits) MaxApps(ctx context.Context, projectID string) (int, error) {
	if l.plans == nil || l.tiers == nil {
		return 0, errNoAppLimits
	}
	tier, err := l.plans.ProjectPlanTier(ctx, projectID)
	if err != nil {
		return 0, fmt.Errorf("%w: %v", ErrOrgTierUnresolved, err)
	}
	tc, err := l.tiers.TierConfig(ctx, tier)
	if err != nil {
		return 0, fmt.Errorf("%w: %v", ErrOrgTierUnresolved, err)
	}
	return tc.MaxApps, nil
}
