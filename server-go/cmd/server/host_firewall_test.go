package main

import "testing"

// Rootless Podman has no guard to wait for, and its healthchecks may not even run (no user systemd timers).
func TestHostFirewallContainerIsNoneOnRootlessPodman(t *testing.T) {
	env := map[string]string{"HOST_FIREWALL_CONTAINER": "excalibase-host-firewall"}
	getenv := func(key string) string { return env[key] }
	if got := hostFirewallContainer(getenv); got != "excalibase-host-firewall" {
		t.Fatalf("docker: %q", got)
	}
	env["ENGINE_SOCKET_UID"] = "0"
	if got := hostFirewallContainer(getenv); got != "" {
		t.Fatalf("rootless Podman: %q", got)
	}
}
