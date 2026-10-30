package service

import (
	"context"
	"fmt"

	"k8s.io/apimachinery/pkg/api/resource"

	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/sitebuilds"
)

type appBuildLimits struct {
	plans PlanTiers
	tiers TierConfigSource
}

// NewAppBuildLimits sizes a project's app git builds by its organisation's current plan (EXC-547).
func NewAppBuildLimits(plans PlanTiers, tiers TierConfigSource) sitebuilds.AppLimitsSource {
	return appBuildLimits{plans: plans, tiers: tiers}
}

func (l appBuildLimits) AppBuildLimits(ctx context.Context, projectID string) (sitebuilds.AppLimits, error) {
	if l.plans == nil || l.tiers == nil {
		return sitebuilds.AppLimits{}, errNoSiteBuildLimits
	}
	tier, err := l.plans.ProjectPlanTier(ctx, projectID)
	if err != nil {
		return sitebuilds.AppLimits{}, fmt.Errorf("%w: %v", ErrOrgTierUnresolved, err)
	}
	tc, err := l.tiers.TierConfig(ctx, tier)
	if err != nil {
		return sitebuilds.AppLimits{}, fmt.Errorf("%w: %v", ErrOrgTierUnresolved, err)
	}
	plan := tc.AppBuilds
	maxImage, err := resource.ParseQuantity(plan.MaxImage)
	if err != nil || maxImage.Value() <= 0 {
		return sitebuilds.AppLimits{}, fmt.Errorf("the %s plan's app build image cap %q is not a size", tier, plan.MaxImage)
	}
	return sitebuilds.AppLimits{Limits: sitebuilds.Limits{CPUMillis: plan.CPUMillis, Memory: plan.Memory, Disk: plan.Disk,
		TimeoutSeconds: plan.TimeoutSeconds, ProjectConcurrency: plan.Concurrency}, MaxImageBytes: maxImage.Value(),
		MaxBuildsPerHour: plan.MaxBuildsPerHour}, nil
}

// LargestAppBuild is the biggest app build any plan runs as the plans stand now; it sizes the app build quota.
func (l appBuildLimits) LargestAppBuild(ctx context.Context) (sitebuilds.BuildSize, error) {
	if l.tiers == nil {
		return sitebuilds.BuildSize{}, errNoSiteBuildLimits
	}
	var largest sitebuilds.BuildSize
	for _, tier := range planTiers {
		tc, err := l.tiers.TierConfig(ctx, tier)
		if err != nil {
			return sitebuilds.BuildSize{}, fmt.Errorf("%w: %v", ErrOrgTierUnresolved, err)
		}
		largest = largerSize(largest, appBuildSize(tc.AppBuilds))
	}
	return largest, nil
}

func appBuildSize(plan config.AppBuildTierLimits) sitebuilds.BuildSize {
	return sitebuilds.BuildSize{CPUMillis: plan.CPUMillis, Memory: plan.Memory, Disk: plan.Disk}
}
