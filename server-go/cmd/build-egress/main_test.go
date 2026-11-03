package main

import (
	"strings"
	"testing"
	"time"
)

func TestLoadSettingsDefaultsAndLists(t *testing.T) {
	env := map[string]string{"EGRESS_EXTRA_DENY_CIDRS": " 203.0.113.0/24 ,", "EGRESS_ALLOW": "git.lan:8080, registry.lan:443", "EGRESS_LIFETIME": "600s"}
	settings := loadSettings(func(name string) string { return env[name] })
	if settings.listen != ":3128" {
		t.Fatalf("listen %q, want :3128", settings.listen)
	}
	if strings.Join(settings.extraDeny, "|") != "203.0.113.0/24" || strings.Join(settings.allow, "|") != "git.lan:8080|registry.lan:443" {
		t.Fatalf("lists %+v", settings)
	}
	if settings.lifetime != 10*time.Minute {
		t.Fatalf("lifetime %v", settings.lifetime)
	}
	server, err := newServer(settings)
	if err != nil {
		t.Fatalf("newServer: %v", err)
	}
	if server.IdleTimeout <= 0 || server.ReadHeaderTimeout <= 0 {
		t.Fatalf("server timeouts idle %v header %v", server.IdleTimeout, server.ReadHeaderTimeout)
	}
}

// A sidecar left behind by a provisioning that died ends at the build's time limit; no lifetime is no limit.
func TestLoadSettingsLifetime(t *testing.T) {
	for raw, want := range map[string]time.Duration{"": 0, "nonsense": 0, "-5s": 0, "90s": 90 * time.Second} {
		got := loadSettings(func(name string) string { return map[string]string{"EGRESS_LIFETIME": raw}[name] }).lifetime
		if got != want {
			t.Fatalf("EGRESS_LIFETIME=%q: %v, want %v", raw, got, want)
		}
	}
}

func TestLoadSettingsRefusesBadValues(t *testing.T) {
	settings := loadSettings(func(name string) string {
		return map[string]string{"EGRESS_ALLOW": "no-port"}[name]
	})
	if _, err := newServer(settings); err == nil {
		t.Fatal("an allow entry without a port was accepted")
	}
}
