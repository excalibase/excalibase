package config

import (
	"testing"

	"github.com/excalibase/provisioning-poc/internal/domain"
)

// The catalog is the only source of an app's cpu and memory: the caller names
// a tier, never a resource string, so nothing a tenant sends can ask for more
// than the tier it pays for.
func TestGetAppTierConfigCoversEveryTier(t *testing.T) {
	for _, tier := range []domain.TierType{domain.Free, domain.Standard, domain.Enterprise} {
		tc, err := GetAppTierConfig(tier)
		if err != nil {
			t.Fatalf("%s: %v", tier, err)
		}
		if tc.CPURequest == "" || tc.CPULimit == "" || tc.MemoryRequest == "" || tc.MemoryLimit == "" {
			t.Errorf("%s: every field must be populated, got %+v", tier, tc)
		}
		if tc.MaxReplicas <= 0 {
			t.Errorf("%s: max replicas must be positive, got %d", tier, tc.MaxReplicas)
		}
	}
}

// An unknown tier is a refusal, never a default — a mistyped tier must not
// silently run on the smallest box the platform happens to offer.
func TestGetAppTierConfigRefusesUnknownTier(t *testing.T) {
	for _, tier := range []domain.TierType{"", "PLATINUM", "free"} {
		if _, err := GetAppTierConfig(tier); err == nil {
			t.Errorf("tier %q must be refused", tier)
		}
	}
}
