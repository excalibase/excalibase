package main

import (
	"context"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/k8s"
)

// smallCluster mirrors the CI runner that refused a 0.5-CPU FREE tenant:
// 2 vCPU, 15% headroom, ~1310m already requested by the platform pods.
func smallCluster() k8s.ClusterCapacity {
	return k8s.ClusterCapacity{
		AllocatableCPUMilli: 2000,
		AllocatableMemBytes: 7 << 30,
		RequestedCPUMilli:   1310,
		RequestedMemBytes:   2 << 30,
		HeadroomPercent:     15,
	}
}

// resolverWithFreeCPU stands in for the tier_configs store: config defaults
// for every tier, with FREE's cpu overridden the way an admin PUT would.
func resolverWithFreeCPU(cpu string) tierResolver {
	return func(_ context.Context, tier domain.TierType) (config.TierConfig, error) {
		tc, err := config.GetTierConfig(tier)
		if err != nil {
			return tc, err
		}
		if tier == domain.Free {
			tc.CPU = cpu
		}
		return tc, nil
	}
}

func TestBuildTierFits_HonoursResolvedTierSpec(t *testing.T) {
	ctx := context.Background()
	capacity := smallCluster()

	withDefault := buildTierFits(ctx, capacity, map[string]int{}, resolverWithFreeCPU("0.5"))
	withSmall := buildTierFits(ctx, capacity, map[string]int{}, resolverWithFreeCPU("0.25"))

	defFit := withDefault["free"]["projectsCanFit"].(int64)
	smallFit := withSmall["free"]["projectsCanFit"].(int64)
	if defFit != 0 {
		t.Fatalf("a 0.5-CPU FREE tenant must not fit the small cluster, got projectsCanFit=%d", defFit)
	}
	if smallFit < 1 {
		t.Fatalf("a 0.25-CPU FREE tenant must fit after the tier edit, got projectsCanFit=%d", smallFit)
	}

	defCPU := withDefault["free"]["perProjectCpuMilli"].(int64)
	smallCPU := withSmall["free"]["perProjectCpuMilli"].(int64)
	if smallCPU >= defCPU {
		t.Fatalf("perProjectCpuMilli must follow the resolved spec: small=%d default=%d", smallCPU, defCPU)
	}
}

func TestCapacityDeps_ResolverFallsBackToConfigDefaults(t *testing.T) {
	d := &capacityDeps{} // nothing wired, as in a deployment without the tier store
	got, err := d.resolver()(context.Background(), domain.Free)
	if err != nil {
		t.Fatalf("fallback resolver: %v", err)
	}
	want, _ := config.GetTierConfig(domain.Free)
	if got.CPU != want.CPU || got.Memory != want.Memory {
		t.Fatalf("fallback must equal config defaults: got cpu=%s mem=%s want cpu=%s mem=%s",
			got.CPU, got.Memory, want.CPU, want.Memory)
	}
}
