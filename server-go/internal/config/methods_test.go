package config

import (
	"testing"

	"github.com/excalibase/provisioning-poc/internal/domain"
)

func TestIsCloud(t *testing.T) {
	if !(AppConfig{DeploymentMode: "cloud"}).IsCloud() {
		t.Error("DeploymentMode=cloud should be cloud")
	}
	if (AppConfig{DeploymentMode: "selfhosted"}).IsCloud() {
		t.Error("DeploymentMode=selfhosted should not be cloud")
	}
	if (AppConfig{}).IsCloud() {
		t.Error("empty DeploymentMode should not be cloud")
	}
}

func TestTierConfigStrings(t *testing.T) {
	tc, err := GetTierConfig(domain.Standard)
	if err != nil {
		t.Fatalf("GetTierConfig: %v", err)
	}
	if tc.CPUString() != tc.CPU {
		t.Errorf("CPUString() = %q, want %q", tc.CPUString(), tc.CPU)
	}
	if tc.MemoryString() != tc.Memory {
		t.Errorf("MemoryString() = %q, want %q", tc.MemoryString(), tc.Memory)
	}
}
