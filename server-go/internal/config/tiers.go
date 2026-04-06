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
		Instances:   3,
		StorageSize: "50Gi",
		Memory:      "4Gi",
		CPU:         "2",
		BackupEnabled: true,
	},
	domain.Enterprise: {
		MaxProjects: 0, // unlimited
		Instances:   5,
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
