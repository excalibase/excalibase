package config

import "fmt"

// How each project's function runtime is fenced (EXC-558).
const (
	// FunctionEgressNetworkPolicy opens public addresses on the allowlisted
	// ports; the host names are held by the runtime's sandbox alone. Any
	// policy-enforcing CNI.
	FunctionEgressNetworkPolicy = "networkpolicy"
	// FunctionEgressCilium admits the allowlisted host names only, through
	// Cilium's DNS proxy. Needs Cilium.
	FunctionEgressCilium = "cilium"
)

// FunctionEgressByName reports whether function runtimes are fenced by host name.
func (c AppConfig) FunctionEgressByName() bool {
	return c.FunctionEgressPolicy == FunctionEgressCilium
}

func (c AppConfig) validateFunctionEgress() error {
	switch c.FunctionEgressPolicy {
	case "", FunctionEgressNetworkPolicy, FunctionEgressCilium:
		return nil
	}
	return fmt.Errorf("FUNCTION_EGRESS_POLICY %q: want %q or %q", c.FunctionEgressPolicy, FunctionEgressNetworkPolicy, FunctionEgressCilium)
}
