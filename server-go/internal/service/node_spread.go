package service

import (
	"context"
	"errors"
	"fmt"
	"log"

	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/k8s"
	"k8s.io/apimachinery/pkg/api/resource"
)

// NotEnoughNodesError refuses a multi-instance tier on a platform with fewer
// usable nodes than instances: the cluster requires one instance per node, so
// it could never finish scheduling.
type NotEnoughNodesError struct {
	Tier      domain.TierType
	Instances int
	Nodes     int
}

func (e *NotEnoughNodesError) Error() string {
	return fmt.Sprintf("the %s plan runs %d database instances, each on its own node, but this platform has only %d node(s) that can take one",
		e.Tier, e.Instances, e.Nodes)
}

// ErrNodePlacementUnknown refuses a multi-instance tier when the nodes could
// not be read: an unanswered lookup is never taken as room.
var ErrNodePlacementUnknown = errors.New("could not check that the platform has a node for each database instance")

// RequireNodesForTier is RequireNodeCount at the tier's current configuration.
func (s *ProvisioningService) RequireNodesForTier(ctx context.Context, tierType domain.TierType) error {
	tier, err := s.tierConfig(ctx, tierType)
	if err != nil {
		return err
	}
	return s.RequireNodeCount(ctx, tierType, tier.Instances)
}

// RequireNodeCount refuses a tier with more instances than the platform has
// nodes that can take one. It asks nothing about current room, so it suits a
// plan change as well as a new cluster. A single instance has nothing to
// spread; without a Kubernetes client (a single Docker/Podman host) there is
// one node (ADR 0038).
func (s *ProvisioningService) RequireNodeCount(ctx context.Context, tierType domain.TierType, instances int) error {
	_, err := s.usableNodes(ctx, tierType, instances)
	return err
}

// RequireNodeSpread is RequireNodeCount plus room on each of those nodes for
// one instance of the tier's size, for a cluster about to be created.
func (s *ProvisioningService) RequireNodeSpread(ctx context.Context, tierType domain.TierType, tier config.TierConfig) error {
	nodes, err := s.usableNodes(ctx, tierType, tier.Instances)
	if err != nil || len(nodes) == 0 {
		return err
	}
	cpu, err := resource.ParseQuantity(tier.CPU)
	if err != nil {
		return fmt.Errorf("tier %s cpu %q: %w", tierType, tier.CPU, err)
	}
	memory, err := resource.ParseQuantity(tier.Memory)
	if err != nil {
		return fmt.Errorf("tier %s memory %q: %w", tierType, tier.Memory, err)
	}
	roomy := 0
	for _, node := range nodes {
		if node.Fits(cpu.MilliValue(), memory.Value(), s.capacityHeadroomPercent) {
			roomy++
		}
	}
	if roomy < tier.Instances {
		log.Printf("INFO: provision refused (per-node room): tier=%s instances=%d nodes with room=%d", tierType, tier.Instances, roomy)
		return errNotEnoughCapacity
	}
	return nil
}

// usableNodes lists the untainted schedulable nodes when the tier has
// instances to spread, refusing when there are fewer than instances. It
// returns nothing, and no error, when there is nothing to spread.
func (s *ProvisioningService) usableNodes(ctx context.Context, tierType domain.TierType, instances int) ([]k8s.NodeCapacity, error) {
	if instances <= 1 {
		return nil, nil
	}
	if s.k8sClient == nil {
		return nil, &NotEnoughNodesError{Tier: tierType, Instances: instances, Nodes: 1}
	}
	capacity, err := s.k8sClient.GetClusterCapacity(ctx)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrNodePlacementUnknown, err)
	}
	usable := make([]k8s.NodeCapacity, 0, len(capacity.Nodes))
	for _, node := range capacity.Nodes {
		if !node.Tainted {
			usable = append(usable, node)
		}
	}
	if len(usable) < instances {
		return nil, &NotEnoughNodesError{Tier: tierType, Instances: instances, Nodes: len(usable)}
	}
	return usable, nil
}
