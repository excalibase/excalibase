package main

import (
	"errors"
	"net/netip"
	"os"
	"path/filepath"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/hostaddr"
	"github.com/excalibase/provisioning-poc/internal/publicaddr"
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

// On a single host the forwarder reads the host's own addresses (EXC-658).
func TestLoadConfig_HostTables(t *testing.T) {
	cfg, err := loadConfig(envOf(map[string]string{
		"PROVISIONING_URL": "http://provisioning:24005", "LOG_FORWARDER_TOKEN": "tok",
		"LOG_FORWARDER_HOST_FIB_TRIE": "/host/fib_trie", "LOG_FORWARDER_HOST_IF_INET6": "/host/if_inet6",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.hostTables.FibTrie != "/host/fib_trie" || cfg.hostTables.IfInet6 != "/host/if_inet6" || cfg.hostTables.Self != "/proc/self/net/fib_trie" {
		t.Errorf("host tables %+v", cfg.hostTables)
	}
	if _, err := loadConfig(envOf(map[string]string{"PROVISIONING_URL": "http://p:1", "LOG_FORWARDER_TOKEN": "tok",
		"LOG_FORWARDER_HOST_IF_INET6": "/host/if_inet6"})); err == nil {
		t.Error("IPv6 table without the routing table accepted")
	}
}

func TestNewDestinationDialer_RefusesTheHostsOwnAddress(t *testing.T) {
	trie := filepath.Join(t.TempDir(), "fib_trie")
	body := "Main:\n  |-- 127.0.0.1\n     /32 host LOCAL\n  |-- 198.51.100.1\n     /32 host LOCAL\n"
	if err := os.WriteFile(trie, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	dialer, hosts, err := newDestinationDialer(config{hostTables: hostaddr.Files{FibTrie: trie}})
	if err != nil || hosts == nil {
		t.Fatalf("dialer: %v", err)
	}
	if err := dialer.Check(netip.MustParseAddr("198.51.100.1")); !errors.Is(err, publicaddr.ErrInternalAddress) {
		t.Errorf("host address: %v", err)
	}
	if err := dialer.Check(netip.MustParseAddr("198.51.100.2")); err != nil {
		t.Errorf("other address: %v", err)
	}
	if _, _, err := newDestinationDialer(config{hostTables: hostaddr.Files{FibTrie: filepath.Join(t.TempDir(), "gone")}}); err == nil {
		t.Error("started without the host's table")
	}
	plain, none, err := newDestinationDialer(config{deny: []netip.Prefix{netip.MustParsePrefix("198.51.100.0/24")}})
	if err != nil || none != nil || plain.Check(netip.MustParseAddr("198.51.100.9")) == nil {
		t.Errorf("Kubernetes dialer: %v %v", none, err)
	}
}
