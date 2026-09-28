package service

import (
	"context"
	"fmt"

	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/k8s"
)

var errNoQuotaTiers = fmt.Errorf("%w: no source for the plan's namespace quota", ErrOrgTierUnresolved)

// SetNamespaceQuotaTiers wires the plan values the namespace quota is sized from (EXC-524).
func (s *AppDeployService) SetNamespaceQuotaTiers(tiers TierConfigSource) { s.quotaTiers = tiers }

// syncNamespaceQuota sizes the project's quota from its plan before a deploy
// creates pods or claims, so the quota never refuses what the plan allows.
// Apps held above the plan's count after a downgrade keep their room.
func (s *AppDeployService) syncNamespaceQuota(ctx context.Context, namespace, projectID string,
	tierType domain.TierType, appTier config.AppTierConfig) error {
	if s.quotaTiers == nil {
		return errNoQuotaTiers
	}
	plan, err := s.quotaTiers.TierConfig(ctx, tierType)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrOrgTierUnresolved, err)
	}
	held, err := s.apps.List(projectID)
	if err != nil {
		return fmt.Errorf("count the project's apps: %w", err)
	}
	quota := k8s.NamespaceQuotaForPlan(k8s.PlanQuotaInputs{
		DBInstances:    plan.Instances,
		Apps:           max(plan.MaxApps, len(held)),
		AppMaxReplicas: appTier.MaxReplicas,
	})
	if err := s.kube.EnsureNamespaceQuota(ctx, namespace, quota); err != nil {
		return fmt.Errorf("size the project's namespace quota: %w", err)
	}
	return nil
}
