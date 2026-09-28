package config

import (
	"testing"

	"github.com/excalibase/provisioning-poc/internal/domain"
)

func TestGetTierConfig(t *testing.T) {
	tests := []struct {
		tier      domain.TierType
		instances int
		storage   string
		memory    string
		cpu       string
	}{
		{domain.Free, 1, "5Gi", "512Mi", "0.5"},
		// Paid tiers: one instance per node, odd counts (owner decision 2026-09-28).
		{domain.Standard, 3, "50Gi", "4Gi", "2"},
		{domain.Enterprise, 5, "500Gi", "16Gi", "4"},
	}

	for _, tt := range tests {
		tc, err := GetTierConfig(tt.tier)
		if err != nil {
			t.Fatalf("GetTierConfig(%s): %v", tt.tier, err)
		}
		if tc.Instances != tt.instances {
			t.Errorf("%s instances: got %d, want %d", tt.tier, tc.Instances, tt.instances)
		}
		if tc.StorageSize != tt.storage {
			t.Errorf("%s storage: got %s, want %s", tt.tier, tc.StorageSize, tt.storage)
		}
		if tc.Memory != tt.memory {
			t.Errorf("%s memory: got %s, want %s", tt.tier, tc.Memory, tt.memory)
		}
		if tc.CPU != tt.cpu {
			t.Errorf("%s cpu: got %s, want %s", tt.tier, tc.CPU, tt.cpu)
		}
	}
}

func TestGetTierConfigUnknown(t *testing.T) {
	_, err := GetTierConfig("UNKNOWN")
	if err == nil {
		t.Error("expected error for unknown tier")
	}
}

func TestGetTierConfig_AutoPauseDefaults(t *testing.T) {
	cases := map[domain.TierType]int{domain.Free: 7, domain.Standard: 0, domain.Enterprise: 0}
	for tier, want := range cases {
		tc, err := GetTierConfig(tier)
		if err != nil {
			t.Fatalf("GetTierConfig(%s): %v", tier, err)
		}
		if tc.AutoPauseAfterDays != want {
			t.Errorf("%s autoPauseAfterDays: got %d, want %d", tier, tc.AutoPauseAfterDays, want)
		}
	}
}

func TestEveryBuiltInTierIsBackedUp(t *testing.T) {
	for _, tier := range []domain.TierType{domain.Free, domain.Standard, domain.Enterprise} {
		tc, err := GetTierConfig(tier)
		if err != nil {
			t.Fatalf("GetTierConfig(%s): %v", tier, err)
		}
		if !tc.BackupEnabled {
			t.Errorf("%s has no backups", tier)
		}
	}
}

// Owner decision 2026-09-28: each plan has a disk it starts with and a disk it
// may grow to. Free is fixed.
func TestTiersHaveAStartingAndAMaximumDisk(t *testing.T) {
	want := map[domain.TierType][2]string{
		domain.Free:       {"5Gi", "5Gi"},
		domain.Standard:   {"50Gi", "500Gi"},
		domain.Enterprise: {"500Gi", "2Ti"},
	}
	for tier, sizes := range want {
		tc, err := GetTierConfig(tier)
		if err != nil {
			t.Fatalf("%s: %v", tier, err)
		}
		if tc.StorageSize != sizes[0] || tc.MaxStorageSize != sizes[1] {
			t.Errorf("%s: start %q max %q, want %q %q", tier, tc.StorageSize, tc.MaxStorageSize, sizes[0], sizes[1])
		}
	}
}
