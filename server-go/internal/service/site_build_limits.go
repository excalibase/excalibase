package service

import (
	"context"
	"fmt"

	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/sitebuilds"
	"k8s.io/apimachinery/pkg/api/resource"
)

var errNoSiteBuildLimits = fmt.Errorf("%w: no source for the plan's build limits", ErrOrgTierUnresolved)

// planTiers are the plans a build may run on; the largest of them sizes the cluster's quota.
var planTiers = []domain.TierType{domain.Free, domain.Standard, domain.Enterprise}

type siteBuildLimits struct {
	plans PlanTiers
	tiers TierConfigSource
}

// NewSiteBuildLimits sizes a project's git builds by its organisation's current plan (EXC-354).
func NewSiteBuildLimits(plans PlanTiers, tiers TierConfigSource) sitebuilds.LimitsSource {
	return siteBuildLimits{plans: plans, tiers: tiers}
}

func (l siteBuildLimits) BuildLimits(ctx context.Context, projectID string) (sitebuilds.Limits, error) {
	if l.plans == nil || l.tiers == nil {
		return sitebuilds.Limits{}, errNoSiteBuildLimits
	}
	tier, err := l.plans.ProjectPlanTier(ctx, projectID)
	if err != nil {
		return sitebuilds.Limits{}, fmt.Errorf("%w: %v", ErrOrgTierUnresolved, err)
	}
	tc, err := l.tiers.TierConfig(ctx, tier)
	if err != nil {
		return sitebuilds.Limits{}, fmt.Errorf("%w: %v", ErrOrgTierUnresolved, err)
	}
	plan := tc.SiteBuilds
	return sitebuilds.Limits{CPUMillis: plan.CPUMillis, Memory: plan.Memory, Disk: plan.Disk,
		TimeoutSeconds: plan.TimeoutSeconds, ProjectConcurrency: plan.Concurrency}, nil
}

// LargestBuild is the biggest build any plan runs as the plans stand now.
func (l siteBuildLimits) LargestBuild(ctx context.Context) (sitebuilds.BuildSize, error) {
	if l.tiers == nil {
		return sitebuilds.BuildSize{}, errNoSiteBuildLimits
	}
	var largest sitebuilds.BuildSize
	for _, tier := range planTiers {
		tc, err := l.tiers.TierConfig(ctx, tier)
		if err != nil {
			return sitebuilds.BuildSize{}, fmt.Errorf("%w: %v", ErrOrgTierUnresolved, err)
		}
		largest = largerSize(largest, siteBuildSize(tc.SiteBuilds))
	}
	return largest, nil
}

func siteBuildSize(plan config.SiteBuildTierLimits) sitebuilds.BuildSize {
	return sitebuilds.BuildSize{CPUMillis: plan.CPUMillis, Memory: plan.Memory, Disk: plan.Disk}
}

func largerSize(current, plan sitebuilds.BuildSize) sitebuilds.BuildSize {
	current.CPUMillis = max(current.CPUMillis, plan.CPUMillis)
	current.Memory = largerQuantity(current.Memory, plan.Memory)
	current.Disk = largerQuantity(current.Disk, plan.Disk)
	return current
}

// largerQuantity keeps the larger size; a plan's size that does not parse never wins.
func largerQuantity(current, candidate string) string {
	candidateSize, err := resource.ParseQuantity(candidate)
	if err != nil {
		return current
	}
	if current == "" {
		return candidate
	}
	currentSize := resource.MustParse(current)
	if candidateSize.Cmp(currentSize) > 0 {
		return candidate
	}
	return current
}
