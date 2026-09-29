package service

import (
	"context"
	"fmt"
	"log"

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

// SyncProjectQuota sizes one project's quota from its organisation's plan now,
// whether or not it runs any app: at project creation, when a database is
// added, and when a plan changes (EXC-524). A project with no namespace yet
// (docker mode, or not created) has none to size.
func (s *AppDeployService) SyncProjectQuota(ctx context.Context, projectID string) error {
	inst, err := s.instances.FindByProjectID(projectID)
	if err != nil {
		return fmt.Errorf("read project %s: %w", projectID, err)
	}
	if inst == nil {
		return fmt.Errorf("project not found: %s", projectID)
	}
	if inst.Namespace == "" || inst.DeploymentMode == domain.ModeDocker {
		return nil
	}
	if s.plans == nil {
		return errNoQuotaTiers
	}
	tierType, err := s.plans.ProjectPlanTier(ctx, projectID)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrOrgTierUnresolved, err)
	}
	appTier, err := config.GetAppTierConfig(tierType)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrOrgTierUnresolved, err)
	}
	return s.syncNamespaceQuota(ctx, inst.Namespace, projectID, tierType, appTier)
}

// SyncAllProjectQuotas re-sizes every project after a plan edit or an org plan
// change; a project that fails is logged and the next change or deploy retries it.
func (s *AppDeployService) SyncAllProjectQuotas(ctx context.Context) {
	instances, err := s.instances.FindAll()
	if err != nil {
		log.Printf("namespace quotas: list projects: %v", err)
		return
	}
	seen := map[string]bool{}
	for _, inst := range instances {
		if seen[inst.ProjectID] {
			continue
		}
		seen[inst.ProjectID] = true
		if err := s.SyncProjectQuota(ctx, inst.ProjectID); err != nil {
			log.Printf("namespace quota of %s: %v", inst.ProjectID, err)
		}
	}
}
