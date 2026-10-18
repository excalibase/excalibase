package service

import (
	"context"
	"log"
	"strings"

	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/domain"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
)

// TierLimits is a tier's row as an admin set it.
type TierLimits func(ctx context.Context, tier domain.TierType) (config.TierConfig, error)

// SetTierLimits is where limits come from for a pod that declares none.
func (s *MetricsService) SetTierLimits(lookup TierLimits) {
	s.tierLimits = lookup
}

// podLimit is what one postgres instance may use.
type podLimit struct {
	cpuCores float64
	memoryMB int64
}

// databaseLimits are each postgres pod's limits as Kubernetes enforces them,
// and the tier row's per-instance limits for a pod that declares none.
type databaseLimits struct {
	perPod    map[string]podLimit
	tier      *podLimit
	instances int
}

func (l databaseLimits) of(pod string) (podLimit, bool) {
	if limit, ok := l.perPod[pod]; ok {
		return limit, true
	}
	if l.tier != nil {
		return *l.tier, true
	}
	return podLimit{}, false
}

// total is the whole database's limit, or false when nothing names one.
func (l databaseLimits) total() (podLimit, int, bool) {
	if len(l.perPod) > 0 {
		var sum podLimit
		for _, limit := range l.perPod {
			sum.cpuCores += limit.cpuCores
			sum.memoryMB += limit.memoryMB
		}
		return sum, len(l.perPod), true
	}
	if l.tier != nil {
		return podLimit{l.tier.cpuCores * float64(l.instances), l.tier.memoryMB * int64(l.instances)}, l.instances, true
	}
	return podLimit{}, 0, false
}

func (s *MetricsService) databaseLimits(ctx context.Context, inst *domain.DatabaseInstance) databaseLimits {
	limits := databaseLimits{perPod: map[string]podLimit{}}
	pods, err := s.k8sClient.GetPods(ctx, inst.Namespace, "cnpg.io/cluster="+inst.ProjectID+postgresSuffix)
	if err != nil {
		log.Printf("metrics of %s: postgres pods: %v", inst.ProjectID, err)
	}
	for _, pod := range pods {
		if !strings.HasPrefix(pod.Name, inst.ProjectID+postgresSuffix+"-") {
			continue
		}
		if limit, ok := postgresContainerLimit(pod); ok {
			limits.perPod[pod.Name] = limit
		}
	}
	if s.tierLimits == nil {
		return limits
	}
	row, err := s.tierLimits(ctx, inst.Tier)
	if err != nil {
		log.Printf("metrics of %s: tier %s: %v", inst.ProjectID, inst.Tier, err)
		return limits
	}
	cpu, cpuErr := resource.ParseQuantity(row.CPU)
	memory, memoryErr := resource.ParseQuantity(row.Memory)
	if cpuErr == nil && memoryErr == nil {
		limits.tier = &podLimit{cpuCores: float64(cpu.MilliValue()) / 1000, memoryMB: memory.Value() / (1024 * 1024)}
		limits.instances = max(row.Instances, 1)
	}
	return limits
}

func postgresContainerLimit(pod corev1.Pod) (podLimit, bool) {
	for _, container := range pod.Spec.Containers {
		if container.Name != "postgres" {
			continue
		}
		cpu, hasCPU := container.Resources.Limits[corev1.ResourceCPU]
		memory, hasMemory := container.Resources.Limits[corev1.ResourceMemory]
		if hasCPU && hasMemory {
			return podLimit{cpuCores: float64(cpu.MilliValue()) / 1000, memoryMB: memory.Value() / (1024 * 1024)}, true
		}
	}
	return podLimit{}, false
}
