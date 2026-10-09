package provisioner

import (
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/config"
)

func TestLimitsForTierReadsTheTiersMemoryAndCPU(t *testing.T) {
	cases := []struct {
		memory, cpu string
		want        ContainerLimits
	}{
		{"512Mi", "0.5", ContainerLimits{MemoryBytes: 512 << 20, NanoCPUs: 500_000_000}},
		{"4Gi", "2", ContainerLimits{MemoryBytes: 4 << 30, NanoCPUs: 2_000_000_000}},
		{"16Gi", "500m", ContainerLimits{MemoryBytes: 16 << 30, NanoCPUs: 500_000_000}},
	}
	for _, tc := range cases {
		got, err := LimitsForTier(config.TierConfig{Memory: tc.memory, CPU: tc.cpu})
		if err != nil {
			t.Fatalf("%s/%s: %v", tc.memory, tc.cpu, err)
		}
		if got != tc.want {
			t.Errorf("%s/%s = %+v, want %+v", tc.memory, tc.cpu, got, tc.want)
		}
	}
}

// No default size: a tier that does not say refuses, as on Kubernetes.
func TestLimitsForTierRefusesATierThatDoesNotSay(t *testing.T) {
	for _, tier := range []config.TierConfig{
		{},
		{Memory: "512Mi"},
		{CPU: "0.5"},
		{Memory: "lots", CPU: "0.5"},
		{Memory: "512Mi", CPU: "fast"},
		{Memory: "0", CPU: "0.5"},
		{Memory: "512Mi", CPU: "-1"},
	} {
		if got, err := LimitsForTier(tier); err == nil {
			t.Errorf("tier %+v gave %+v, want refused", tier, got)
		}
	}
}

// Memory caps swap too: a container at its limit must not spill into host swap.
func TestResourcesCapMemoryAndSwapTogether(t *testing.T) {
	res := ContainerLimits{MemoryBytes: 512 << 20, NanoCPUs: 500_000_000}.resources()
	if res.Memory != 512<<20 || res.MemorySwap != 512<<20 || res.NanoCPUs != 500_000_000 {
		t.Fatalf("resources = memory %d swap %d cpus %d", res.Memory, res.MemorySwap, res.NanoCPUs)
	}
}

// ADR 0038: copies on one machine share its disk and kernel, so a tier with
// more than one copy is refused rather than quietly run as one.
func TestLimitsForTierRefusesMoreThanOneCopy(t *testing.T) {
	for _, copies := range []int{2, 3, 5} {
		_, err := LimitsForTier(config.TierConfig{Memory: "4Gi", CPU: "2", Instances: copies})
		if err == nil || !strings.Contains(err.Error(), "machines") {
			t.Errorf("%d copies: err = %v, want refused naming machines", copies, err)
		}
	}
	for _, copies := range []int{0, 1} {
		if _, err := LimitsForTier(config.TierConfig{Memory: "4Gi", CPU: "2", Instances: copies}); err != nil {
			t.Errorf("%d copies refused: %v", copies, err)
		}
	}
}
