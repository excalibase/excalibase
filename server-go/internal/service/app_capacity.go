package service

import (
	"context"
	"errors"
	"fmt"
	"log"

	"github.com/excalibase/provisioning-poc/internal/apphost"
	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/k8s"
	"github.com/excalibase/provisioning-poc/internal/storage"
	"k8s.io/apimachinery/pkg/api/resource"
)

var (
	// ErrAppCapacity refuses a rollout the cluster has no room for; the numbers go to the log only.
	ErrAppCapacity = errors.New("there is no room to run the app right now; try again later")
	// ErrAppOverPlan refuses more copies than the organisation's plan allows.
	ErrAppOverPlan = errors.New("the plan does not allow this many copies")
)

// PlanTiers answers which plan sizes a project's apps: its organisation's, as it is now.
type PlanTiers interface {
	ProjectPlanTier(ctx context.Context, projectID string) (domain.TierType, error)
}

type OrgFinder interface {
	FindOrgByID(ctx context.Context, id string) (*domain.Org, error)
}

type orgPlanTiers struct {
	instances storage.InstanceStore
	orgs      OrgFinder
}

func NewOrgPlanTiers(instances storage.InstanceStore, orgs OrgFinder) PlanTiers {
	return orgPlanTiers{instances: instances, orgs: orgs}
}

func (p orgPlanTiers) ProjectPlanTier(ctx context.Context, projectID string) (domain.TierType, error) {
	inst, err := p.instances.FindByProjectID(projectID)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrOrgTierUnresolved, err)
	}
	if inst == nil || inst.OrgID == "" {
		return "", fmt.Errorf("%w: project has no organisation", ErrOrgTierUnresolved)
	}
	org, err := p.orgs.FindOrgByID(ctx, inst.OrgID)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrOrgTierUnresolved, err)
	}
	if org == nil || !domain.IsValidTier(org.Tier) {
		return "", fmt.Errorf("%w: organisation missing or on an unknown plan", ErrOrgTierUnresolved)
	}
	return org.Tier, nil
}

func (s *AppDeployService) SetPlanTiers(plans PlanTiers) { s.plans = plans }

// SetCapacityHeadroom holds back this share of the cluster from apps, as it is from databases.
func (s *AppDeployService) SetCapacityHeadroom(percent int) { s.headroomPercent = percent }

// planTier sizes the rollout by the plan the organisation is on now, so an
// older deploy rolled out again cannot keep a size the organisation left.
func (s *AppDeployService) planTier(ctx context.Context, projectID string, replicas int) (domain.TierType, config.AppTierConfig, error) {
	if s.plans == nil {
		return "", config.AppTierConfig{}, fmt.Errorf("%w: no plan source", ErrOrgTierUnresolved)
	}
	tierType, err := s.plans.ProjectPlanTier(ctx, projectID)
	if err != nil {
		return "", config.AppTierConfig{}, err
	}
	tier, err := config.GetAppTierConfig(tierType)
	if err != nil {
		return "", config.AppTierConfig{}, fmt.Errorf("%w: %v", ErrOrgTierUnresolved, err)
	}
	if replicas > tier.MaxReplicas {
		return "", config.AppTierConfig{}, fmt.Errorf("%w: the %s plan allows at most %d", ErrAppOverPlan, tierType, tier.MaxReplicas)
	}
	return tierType, tier, nil
}

// admit refuses a rollout the cluster has no room for, and anything the cluster
// cannot answer refuses rather than admits. A rolling update runs at most one
// pod over the replica count, so the peak is that many pods of the larger of
// the new and old sizes; the app's live pods already count as requested.
// Pods still terminating keep their requests until they exit, so they are
// never counted as room. Other deploys still rolling out are charged their
// full size, since their new pods may not exist yet.
func (s *AppDeployService) admit(ctx context.Context, namespace string, deploy *apphost.Deploy, tier config.AppTierConfig, replicas int) error {
	if replicas == 0 {
		return nil
	}
	placement, err := s.kube.RuntimeClassPlacement(ctx, s.render.RuntimeClass)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrAppCapacity, err)
	}
	podCPU, podMem, err := requestFootprint(tier.CPURequest, tier.MemoryRequest, placement)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrAppCapacity, err)
	}
	live, err := s.kube.LiveAppPods(ctx, namespace, deploy.AppID)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrAppCapacity, err)
	}
	needCPU, needMem, perCPU, perMem := rolloutNeed(live, replicas, podCPU, podMem)
	reservedCPU, reservedMem, err := s.reservedByOtherRollouts(deploy, placement)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrAppCapacity, err)
	}
	capacity, err := s.kube.GetClusterCapacity(ctx)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrAppCapacity, err)
	}
	if capacity.AllocatableCPUMilli == 0 || capacity.AllocatableMemBytes == 0 {
		return fmt.Errorf("%w: the cluster reports no allocatable capacity", ErrAppCapacity)
	}
	capacity.HeadroomPercent = s.headroomPercent
	if capacity.FreeCPUMilli()-reservedCPU < needCPU || capacity.FreeMemBytes()-reservedMem < needMem ||
		!anyNodeFits(capacity, placement.NodeSelector, perCPU, perMem) {
		log.Printf("INFO: app %s refused: need %dm/%d (one pod %dm/%d) free %dm/%d reserved %dm/%d headroom %d%%",
			deploy.AppID, needCPU, needMem, perCPU, perMem, capacity.FreeCPUMilli(), capacity.FreeMemBytes(),
			reservedCPU, reservedMem, capacity.HeadroomPercent)
		return ErrAppCapacity
	}
	return nil
}

func rolloutNeed(live k8s.AppPods, replicas int, podCPU, podMem int64) (needCPU, needMem, perCPU, perMem int64) {
	surge := 0
	if live.Count > 0 {
		surge = 1
	}
	peak := int64(max(live.Count, replicas+surge))
	perCPU, perMem = max(podCPU, live.MaxCPUMilli), max(podMem, live.MaxMemBytes)
	return max(peak*perCPU-live.CPUMilli, 0), max(peak*perMem-live.MemBytes, 0), podCPU, podMem
}

func anyNodeFits(capacity k8s.ClusterCapacity, selector map[string]string, cpu, mem int64) bool {
	for _, node := range capacity.Nodes {
		if node.Matches(selector) && node.Fits(cpu, mem, capacity.HeadroomPercent) {
			return true
		}
	}
	return false
}

// reservedByOtherRollouts charges every other app's unfinished deploy its full
// size, so two deploys admitted together cannot both count the same room.
func (s *AppDeployService) reservedByOtherRollouts(self *apphost.Deploy, placement k8s.RuntimePlacement) (cpuMilli, memBytes int64, err error) {
	unfinished, err := s.deploys.ListUnfinished()
	if err != nil {
		return 0, 0, fmt.Errorf("list unfinished deploys: %w", err)
	}
	for _, other := range unfinished {
		if other.ID == self.ID || other.AppID == self.AppID || other.Spec.Replicas == 0 {
			continue
		}
		cpu, mem, err := requestFootprint(other.Spec.Resources.CPURequest, other.Spec.Resources.MemoryRequest, placement)
		if err != nil {
			return 0, 0, fmt.Errorf("deploy %s: %w", other.ID, err)
		}
		pods := int64(other.Spec.Replicas + 1)
		cpuMilli += pods * cpu
		memBytes += pods * mem
	}
	return cpuMilli, memBytes, nil
}

// requestFootprint is one pod as the scheduler charges it: its requests plus the sandbox's overhead.
func requestFootprint(cpuRequest, memRequest string, placement k8s.RuntimePlacement) (cpuMilli, memBytes int64, err error) {
	cpu, err := resource.ParseQuantity(cpuRequest)
	if err != nil {
		return 0, 0, fmt.Errorf("parse cpu request %q: %w", cpuRequest, err)
	}
	mem, err := resource.ParseQuantity(memRequest)
	if err != nil {
		return 0, 0, fmt.Errorf("parse memory request %q: %w", memRequest, err)
	}
	return cpu.MilliValue() + placement.OverheadCPUMilli, mem.Value() + placement.OverheadMemBytes, nil
}
