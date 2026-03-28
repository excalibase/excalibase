package config

import (
	"fmt"

	"github.com/excalibase/provisioning-poc/internal/domain"
)

type TierConfig struct {
	Instances   int
	StorageSize string
	Memory      string
	CPU         string
}

var tiers = map[domain.TierType]TierConfig{
	domain.Free: {
		Instances:   1,
		StorageSize: "5Gi",
		Memory:      "512Mi",
		CPU:         "0.5",
	},
	domain.Standard: {
		Instances:   3,
		StorageSize: "50Gi",
		Memory:      "4Gi",
		CPU:         "2",
	},
	domain.Enterprise: {
		Instances:   5,
		StorageSize: "500Gi",
		Memory:      "16Gi",
		CPU:         "4",
	},
}

func GetTierConfig(tier domain.TierType) (TierConfig, error) {
	tc, ok := tiers[tier]
	if !ok {
		return TierConfig{}, fmt.Errorf("unknown tier: %s", tier)
	}
	return tc, nil
}
