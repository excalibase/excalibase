package config

import (
	"fmt"

	"github.com/excalibase/provisioning-poc/internal/domain"
)

type TierConfig struct {
	MaxProjects int
	Instances   int
	StorageSize string
	Memory      string
	CPU         string
	BackupEnabled bool
}

// CPUString returns CPU as configured (e.g. "0.5", "2"). Provided as a method
// so error messages don't leak the raw struct field name.
func (t TierConfig) CPUString() string { return t.CPU }

// MemoryString returns Memory as configured (e.g. "512Mi", "4Gi").
func (t TierConfig) MemoryString() string { return t.Memory }

// Tenant CNPG clusters are single-instance (no HA) — tiers differ only by
// CPU / memory / storage, not replica count. HA (multi-instance) needs a
// multi-node cluster + anti-affinity and is out of scope for the alpha.
var tiers = map[domain.TierType]TierConfig{
	domain.Free: {
		MaxProjects: 1,
		Instances:   1,
		StorageSize: "5Gi",
		Memory:      "512Mi",
		CPU:         "0.5",
		BackupEnabled: false,
	},
	domain.Standard: {
		MaxProjects: 5,
		Instances:   1,
		StorageSize: "50Gi",
		Memory:      "4Gi",
		CPU:         "2",
		BackupEnabled: true,
	},
	domain.Enterprise: {
		MaxProjects: 0, // unlimited
		Instances:   1,
		StorageSize: "500Gi",
		Memory:      "16Gi",
		CPU:         "4",
		BackupEnabled: true,
	},
}

func GetTierConfig(tier domain.TierType) (TierConfig, error) {
	tc, ok := tiers[tier]
	if !ok {
		return TierConfig{}, fmt.Errorf("unknown tier: %s", tier)
	}
	return tc, nil
}
