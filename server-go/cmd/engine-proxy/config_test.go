package main

import (
	"reflect"
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/engineproxy"
)

func env(values map[string]string) func(string) string {
	return func(key string) string { return values[key] }
}

func TestLoadConfigReadsThePolicyFromTheEnvironment(t *testing.T) {
	cfg, err := loadConfig(env(map[string]string{
		"ENGINE_SOCKET":        "/run/podman/podman.sock",
		"PROXY_NETWORKS":       "excalibase-tenants, excalibase-x",
		"PROXY_NETWORK_PREFIX": "excalibase-proj-",
		"PROXY_PORT_BIND_IPS":  "127.0.0.1,0.0.0.0",
		"PROXY_RUNTIMES":       "runsc",
		"PROXY_VOLUME_PREFIX":  "excalibase-proj-",
	}))
	if err != nil {
		t.Fatal(err)
	}
	want := engineproxy.Policy{
		ManagedLabel:    engineproxy.ManagedLabel,
		Networks:        []string{"excalibase-tenants", "excalibase-x"},
		NetworkPrefix:   "excalibase-proj-",
		PortBindIPs:     []string{"127.0.0.1", "0.0.0.0"},
		Runtimes:        []string{"runsc"},
		VolumePrefix:    "excalibase-proj-",
		ContainerPrefix: "excalibase-",
	}
	if !reflect.DeepEqual(cfg.policy, want) {
		t.Fatalf("policy = %+v\nwant %+v", cfg.policy, want)
	}
	if cfg.socket != "/run/podman/podman.sock" || cfg.listen != ":2375" {
		t.Fatalf("socket %q listen %q", cfg.socket, cfg.listen)
	}
}

func TestLoadConfigDefaultsToLoopbackPortsAndTheDockerSocket(t *testing.T) {
	cfg, err := loadConfig(env(map[string]string{"PROXY_NETWORKS": "excalibase-tenants"}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.socket != "/var/run/docker.sock" || !reflect.DeepEqual(cfg.policy.PortBindIPs, []string{"127.0.0.1"}) {
		t.Fatalf("defaults: socket %q ports %v", cfg.socket, cfg.policy.PortBindIPs)
	}
	if len(cfg.policy.Runtimes) != 0 || cfg.policy.NetworkPrefix != "" {
		t.Fatalf("runtimes %v prefix %q, want none", cfg.policy.Runtimes, cfg.policy.NetworkPrefix)
	}
}

func TestLoadConfigRefusesNoNetworks(t *testing.T) {
	if _, err := loadConfig(env(nil)); err == nil || !strings.Contains(err.Error(), "PROXY_NETWORKS") {
		t.Fatalf("err = %v, want PROXY_NETWORKS required", err)
	}
}

func TestLoadConfigRefusesAPrefixThatAdmitsEverything(t *testing.T) {
	for _, key := range []string{"PROXY_NETWORK_PREFIX", "PROXY_VOLUME_PREFIX"} {
		for _, prefix := range []string{"e", "excalibase-", "excalibase", "excalibase-platform"} {
			_, err := loadConfig(env(map[string]string{"PROXY_NETWORKS": "x", key: prefix}))
			if err == nil {
				t.Errorf("%s %q accepted", key, prefix)
			}
		}
	}
}
