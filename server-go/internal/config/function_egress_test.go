package config

import (
	"strings"
	"testing"
)

func TestFunctionEgressPolicyDefaultsToNetworkPolicy(t *testing.T) {
	t.Setenv("FUNCTION_EGRESS_POLICY", "")
	cfg := Load()
	if cfg.FunctionEgressPolicy != FunctionEgressNetworkPolicy || cfg.FunctionEgressByName() {
		t.Fatalf("default = %q (by name %v), want the NetworkPolicy fence", cfg.FunctionEgressPolicy, cfg.FunctionEgressByName())
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("the default must pass: %v", err)
	}
}

// Cilium fences functions by host name (EXC-558); the install says so, it is never guessed.
func TestFunctionEgressPolicyCilium(t *testing.T) {
	t.Setenv("FUNCTION_EGRESS_POLICY", "cilium")
	cfg := Load()
	if !cfg.FunctionEgressByName() {
		t.Fatalf("FUNCTION_EGRESS_POLICY=cilium must fence functions by name, got %q", cfg.FunctionEgressPolicy)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("cilium must pass: %v", err)
	}
}

func TestFunctionEgressPolicyRefusesAnythingElse(t *testing.T) {
	for _, value := range []string{"Cilium", "calico", "none"} {
		cfg := AppConfig{ProvisionerMode: "k8s", DeploymentMode: "cloud", FunctionEgressPolicy: value}
		err := cfg.Validate()
		if err == nil || !strings.Contains(err.Error(), "FUNCTION_EGRESS_POLICY") {
			t.Errorf("%q must be refused naming FUNCTION_EGRESS_POLICY, got %v", value, err)
		}
	}
}
