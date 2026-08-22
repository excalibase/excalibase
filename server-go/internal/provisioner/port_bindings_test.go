package provisioner

import "testing"

// Tenant DB ports must default to the loopback interface, not 0.0.0.0 — a
// docker-published DB on 0.0.0.0 is reachable on the host's LAN/public
// interface (password-only). Operators opt into public exposure explicitly.
func TestPortBindings_DefaultsToLoopback(t *testing.T) {
	_, pm, err := portBindings(map[string]string{"5432": "54321"}, "")
	if err != nil {
		t.Fatalf("portBindings: %v", err)
	}
	binds := pm["5432/tcp"]
	if len(binds) != 1 {
		t.Fatalf("expected one binding, got %d", len(binds))
	}
	if binds[0].HostIP != "127.0.0.1" {
		t.Errorf("default HostIP must be 127.0.0.1 (internal), got %q", binds[0].HostIP)
	}
	if binds[0].HostPort != "54321" {
		t.Errorf("HostPort = %q, want 54321", binds[0].HostPort)
	}
}

func TestPortBindings_PublicWhenRequested(t *testing.T) {
	_, pm, err := portBindings(map[string]string{"5432": "54321"}, "0.0.0.0")
	if err != nil {
		t.Fatalf("portBindings: %v", err)
	}
	if pm["5432/tcp"][0].HostIP != "0.0.0.0" {
		t.Errorf("explicit 0.0.0.0 bind must be honored, got %q", pm["5432/tcp"][0].HostIP)
	}
}

func TestPortBindings_InvalidPort(t *testing.T) {
	if _, _, err := portBindings(map[string]string{"not-a-port": "1"}, ""); err == nil {
		t.Error("expected error for invalid container port")
	}
}
