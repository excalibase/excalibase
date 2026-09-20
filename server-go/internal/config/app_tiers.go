package config

import (
	"fmt"

	"github.com/excalibase/provisioning-poc/internal/domain"
)

// AppTierConfig is the container-resource envelope one tier grants a
// customer application (EXC-377). It is a fixed catalog for the same reason
// the database tiers are: a caller names a tier, never a cpu or memory
// string, so nothing a tenant sends can ask the scheduler for more than the
// tier it pays for. The workload renderer (EXC-379) reads these values; this
// package only decides them.
type AppTierConfig struct {
	CPURequest    string
	CPULimit      string
	MemoryRequest string
	MemoryLimit   string
	// MaxReplicas caps the replica count for the tier. The resource model
	// caps replicas globally too; the tier cap is the tighter of the two.
	MaxReplicas int
}

// Requests are deliberately below limits: an app spends most of its life
// idle, and requesting the burst ceiling would reserve a node's capacity for
// a container that never uses it.
var appTiers = map[domain.TierType]AppTierConfig{
	domain.Free: {
		CPURequest:    "50m",
		CPULimit:      "250m",
		MemoryRequest: "128Mi",
		MemoryLimit:   "256Mi",
		MaxReplicas:   1,
	},
	domain.Standard: {
		CPURequest:    "250m",
		CPULimit:      "1",
		MemoryRequest: "512Mi",
		MemoryLimit:   "1Gi",
		MaxReplicas:   3,
	},
	domain.Enterprise: {
		CPURequest:    "1",
		CPULimit:      "2",
		MemoryRequest: "2Gi",
		MemoryLimit:   "4Gi",
		MaxReplicas:   3,
	},
}

// GetAppTierConfig returns the resource envelope for a tier. An unknown tier
// is an error rather than a default: a mistyped tier must not silently run on
// whichever box the platform happens to offer.
func GetAppTierConfig(tier domain.TierType) (AppTierConfig, error) {
	tc, ok := appTiers[tier]
	if !ok {
		return AppTierConfig{}, fmt.Errorf("unknown app tier: %s", tier)
	}
	return tc, nil
}
