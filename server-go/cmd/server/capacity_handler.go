package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/k8s"
	"github.com/excalibase/provisioning-poc/internal/service"
	"github.com/excalibase/provisioning-poc/internal/storage"
)

// tierResolver returns the resource spec for a tier. Wired to
// ProvisioningService.TierConfig so the capacity report reads the same
// tier_configs table admission does; a nil resolver falls back to the
// hardcoded config defaults.
type tierResolver func(ctx context.Context, tier domain.TierType) (config.TierConfig, error)

// capacityDeps holds the dependencies needed by the capacity HTTP handler.
// Extracted from main() to keep its cognitive complexity below the threshold.
type capacityDeps struct {
	k8sClient       k8s.KubeClient
	store           storage.InstanceStore
	headroomPercent int
	resolveTier     tierResolver
}

// resolver returns the wired tier resolver, or the config-default fallback
// when none was injected (keeps the report working in deployments that never
// set up the tier store).
func (d *capacityDeps) resolver() tierResolver {
	if d.resolveTier != nil {
		return d.resolveTier
	}
	return func(_ context.Context, tier domain.TierType) (config.TierConfig, error) {
		return config.GetTierConfig(tier)
	}
}

// serveCapacity handles GET /api/capacity.
// It returns current cluster CPU/memory headroom and per-tier project fit estimates.
func (d *capacityDeps) serveCapacity(w http.ResponseWriter, r *http.Request) {
	if d.k8sClient == nil {
		http.Error(w, `{"error":"capacity unavailable in this deployment mode"}`, http.StatusNotImplemented)
		return
	}
	capacity, err := d.k8sClient.GetClusterCapacity(r.Context())
	if err != nil {
		http.Error(w, fmt.Sprintf(`{"error":%q}`, err.Error()), http.StatusServiceUnavailable)
		return
	}
	capacity.HeadroomPercent = d.headroomPercent

	instances, _ := d.store.FindAll()
	byTier := buildByTierMap(instances)
	fits := buildTierFits(r.Context(), capacity, byTier, d.resolver())

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"allocatableCpuMilli": capacity.AllocatableCPUMilli,
		"allocatableMemBytes": capacity.AllocatableMemBytes,
		"usableCpuMilli":      capacity.UsableCPUMilli(),
		"usableMemBytes":      capacity.UsableMemBytes(),
		"headroomPercent":     capacity.HeadroomPercent,
		"requestedCpuMilli":   capacity.RequestedCPUMilli,
		"requestedMemBytes":   capacity.RequestedMemBytes,
		"freeCpuMilli":        capacity.FreeCPUMilli(),
		"freeMemBytes":        capacity.FreeMemBytes(),
		"projects": map[string]interface{}{
			"total":  len(instances),
			"byTier": byTier,
		},
		"tiers": fits,
	})
}

// buildByTierMap counts provisioned instances by tier label.
func buildByTierMap(instances []*domain.DatabaseInstance) map[string]int {
	byTier := map[string]int{}
	for _, inst := range instances {
		byTier[string(inst.Tier)]++
	}
	return byTier
}

// buildTierFits computes per-tier capacity estimates (projects that can still
// fit). Tier specs come from the resolver so an admin edit to tier_configs is
// reflected here without a redeploy.
func buildTierFits(ctx context.Context, capacity k8s.ClusterCapacity, byTier map[string]int, resolve tierResolver) map[string]map[string]interface{} {
	tiers := map[string]domain.TierType{
		"free":       domain.Free,
		"standard":   domain.Standard,
		"enterprise": domain.Enterprise,
	}
	fits := map[string]map[string]interface{}{}
	for label, t := range tiers {
		tc, err := resolve(ctx, t)
		if err != nil {
			continue
		}
		cpu, mem, err := service.TierResourceFootprint(tc)
		if err != nil || cpu == 0 || mem == 0 {
			continue
		}
		fitting, limitedBy := calcFitting(capacity, cpu, mem)
		fits[label] = map[string]interface{}{
			"projectsCanFit":       fitting,
			"perProjectCpuMilli":   cpu,
			"perProjectMemBytes":   mem,
			"limitedBy":            limitedBy,
			"currentlyProvisioned": byTier[strings.ToUpper(label)],
		}
	}
	return fits
}

// calcFitting returns how many projects of the given CPU/mem footprint fit in
// the cluster's free headroom, and which resource is the bottleneck.
func calcFitting(capacity k8s.ClusterCapacity, cpu, mem int64) (int64, string) {
	byCPU := capacity.FreeCPUMilli() / cpu
	byMem := capacity.FreeMemBytes() / mem
	fitting := byCPU
	limitedBy := "cpu"
	if byMem < byCPU {
		fitting = byMem
		limitedBy = "memory"
	}
	if fitting < 0 {
		fitting = 0
	}
	return fitting, limitedBy
}
