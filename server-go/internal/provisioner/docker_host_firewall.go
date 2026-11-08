package provisioner

import (
	"context"
	"errors"
	"fmt"
)

// ErrHostFirewallDown is a start the engine proxy refused because the single
// host's firewall is not healthy (EXC-683). It says nothing about the container:
// a caller waits or leaves its work open, and treats no data as lost.
var ErrHostFirewallDown = errors.New("the host firewall is not healthy")

// HostFirewallChecker is a client that can read the firewall tenants wait for.
type HostFirewallChecker interface {
	HostFirewallHealthy(ctx context.Context) (bool, error)
}

// RequireHostFirewall answers ErrHostFirewallDown unless a tenant could start
// now. An operation that removes or stops something it must start again calls
// it first. An engine with no such firewall has nothing to wait for.
func RequireHostFirewall(ctx context.Context, docker DockerClient) error {
	checker, ok := docker.(HostFirewallChecker)
	if !ok {
		return nil
	}
	healthy, err := checker.HostFirewallHealthy(ctx)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrHostFirewallDown, err)
	}
	if !healthy {
		return ErrHostFirewallDown
	}
	return nil
}

// WaitHostFirewall waits within poller for a tenant to be able to start.
func WaitHostFirewall(ctx context.Context, docker DockerClient, poller Poller) error {
	err := poller.WaitUntilReady(ctx, "the host firewall", func(ctx context.Context) (bool, error) {
		return RequireHostFirewall(ctx, docker) == nil, nil
	})
	if err != nil {
		return fmt.Errorf("%w: %v", ErrHostFirewallDown, err)
	}
	return nil
}
