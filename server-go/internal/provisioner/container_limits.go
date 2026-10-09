package provisioner

import (
	"fmt"

	"github.com/docker/docker/api/types/container"
	"github.com/excalibase/provisioning-poc/internal/config"
	"k8s.io/apimachinery/pkg/api/resource"
)

// ContainerLimits is the memory and CPU a Docker or Podman container may use.
type ContainerLimits struct {
	MemoryBytes int64
	NanoCPUs    int64
}

// LimitsForTier reads the tier's memory and CPU the way Kubernetes does. A tier
// that does not state both is refused: there is no default size. So is one with
// more than one copy (ADR 0038): copies on one machine fail together.
func LimitsForTier(tier config.TierConfig) (ContainerLimits, error) {
	if tier.Instances > 1 {
		return ContainerLimits{}, fmt.Errorf("the tier runs %d copies, which need %d machines; a single host runs one copy", tier.Instances, tier.Instances)
	}
	memory, err := positiveQuantity("memory", tier.Memory)
	if err != nil {
		return ContainerLimits{}, err
	}
	cpu, err := positiveQuantity("cpu", tier.CPU)
	if err != nil {
		return ContainerLimits{}, err
	}
	return ContainerLimits{MemoryBytes: memory.Value(), NanoCPUs: cpu.ScaledValue(resource.Nano)}, nil
}

func positiveQuantity(name, raw string) (resource.Quantity, error) {
	if raw == "" {
		return resource.Quantity{}, fmt.Errorf("the tier states no %s", name)
	}
	quantity, err := resource.ParseQuantity(raw)
	if err != nil {
		return resource.Quantity{}, fmt.Errorf("the tier's %s %q cannot be read", name, raw)
	}
	if quantity.Sign() <= 0 {
		return resource.Quantity{}, fmt.Errorf("the tier's %s %q is not positive", name, raw)
	}
	return quantity, nil
}

// resources caps swap at the memory limit, so a container at its limit is
// killed rather than pushing the host into swap.
func (l ContainerLimits) resources() container.Resources {
	return container.Resources{Memory: l.MemoryBytes, MemorySwap: l.MemoryBytes, NanoCPUs: l.NanoCPUs}
}
