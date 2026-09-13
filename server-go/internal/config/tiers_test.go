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
		{domain.Free, 1, "5Gi", "512Mi", "0.25"},
		{domain.Standard, 1, "50Gi", "4Gi", "2"},
		{domain.Enterprise, 1, "500Gi", "16Gi", "4"},
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
