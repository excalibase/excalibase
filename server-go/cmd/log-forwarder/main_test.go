package main

import (
	"net/netip"
	"testing"
)

func envOf(values map[string]string) func(string) string {
	return func(key string) string { return values[key] }
}

func TestLoadConfig(t *testing.T) {
	cfg, err := loadConfig(envOf(map[string]string{
		"PROVISIONING_URL":               "http://provisioning.platform.svc.cluster.local:24005",
		"LOG_FORWARDER_TOKEN":            "tok",
		"LOG_FORWARDER_EXTRA_DENY_CIDRS": " 198.51.100.0/24 , 2001:db8::/32 ",
	}))
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if cfg.provisioningURL != "http://provisioning.platform.svc.cluster.local:24005" || cfg.token != "tok" {
		t.Errorf("cfg %+v", cfg)
	}
	want := []netip.Prefix{netip.MustParsePrefix("198.51.100.0/24"), netip.MustParsePrefix("2001:db8::/32")}
	if len(cfg.deny) != 2 || cfg.deny[0] != want[0] || cfg.deny[1] != want[1] {
		t.Errorf("deny %v", cfg.deny)
	}
}

func TestLoadConfig_Refuses(t *testing.T) {
	cases := map[string]map[string]string{
		"no url":    {"LOG_FORWARDER_TOKEN": "tok"},
		"bad url":   {"PROVISIONING_URL": "ftp://x", "LOG_FORWARDER_TOKEN": "tok"},
		"no token":  {"PROVISIONING_URL": "http://p:1"},
		"bad cidrs": {"PROVISIONING_URL": "http://p:1", "LOG_FORWARDER_TOKEN": "tok", "LOG_FORWARDER_EXTRA_DENY_CIDRS": "10.0.0.0/8,nope"},
	}
	for name, env := range cases {
		if _, err := loadConfig(envOf(env)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}
