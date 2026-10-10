package config

import (
	"strings"
	"testing"
)

func singleHostHosting() AppConfig {
	return AppConfig{
		ProvisionerMode: "docker", DeploymentMode: "selfhosted", AppHostingEnabled: true,
		AppDomain: "apps.example.com", AppDiskToolsImage: "docker.io/excalibase/provisioning:1.4.0",
		SingleHostApps: SingleHostAppsConfig{
			SandboxRuntime: "runsc", NetworkPrefix: "excalibase-net-", VolumePrefix: "excalibase-vol-",
			EdgeContainer: "excalibase-edge", EdgeRoutesDir: "/var/lib/excalibase/app-routes",
			EdgeTLSAddress: "edge:443", ProbeBinary: "/usr/local/bin/excalibase-app-probe", EdgeTLS: "internal",
		},
	}
}

func TestLoadSingleHostApps(t *testing.T) {
	t.Setenv("APP_SANDBOX_RUNTIME", "none")
	t.Setenv("APP_EGRESS", "internet")
	t.Setenv("APP_NETWORK_PREFIX", "excalibase-net-")
	t.Setenv("APP_EDGE_TLS_ADDR", "edge:443")
	t.Setenv("APP_EDGE_TLS", "internal")
	cfg := Load().SingleHostApps
	if cfg.SandboxRuntime != "" || !cfg.Egress || cfg.NetworkPrefix != "excalibase-net-" || cfg.EdgeTLSAddress != "edge:443" ||
		!cfg.EdgeIssuesLocally() {
		t.Fatalf("loaded %+v", cfg)
	}
	t.Setenv("APP_SANDBOX_RUNTIME", "")
	t.Setenv("APP_EGRESS", "")
	cfg = Load().SingleHostApps
	if cfg.SandboxRuntime != "runsc" || cfg.Egress || cfg.ProbeBinary != defaultAppProbeBinary {
		t.Fatalf("defaults %+v", cfg)
	}
}

func TestValidateSingleHostAppHosting(t *testing.T) {
	if err := singleHostHosting().Validate(); err != nil {
		t.Fatalf("a complete single-host config must pass without the Kubernetes ingress settings: %v", err)
	}
	cases := map[string]func(*AppConfig){
		"APP_DOMAIN":           func(c *AppConfig) { c.AppDomain = "" },
		"APP_NETWORK_PREFIX":   func(c *AppConfig) { c.SingleHostApps.NetworkPrefix = "" },
		"APP_VOLUME_PREFIX":    func(c *AppConfig) { c.SingleHostApps.VolumePrefix = "" },
		"APP_EDGE_CONTAINER":   func(c *AppConfig) { c.SingleHostApps.EdgeContainer = "" },
		"APP_EDGE_ROUTES_DIR":  func(c *AppConfig) { c.SingleHostApps.EdgeRoutesDir = "relative/dir" },
		"APP_EDGE_TLS_ADDR":    func(c *AppConfig) { c.SingleHostApps.EdgeTLSAddress = "edge" },
		"APP_PROBE_BINARY":     func(c *AppConfig) { c.SingleHostApps.ProbeBinary = "" },
		"APP_DISK_TOOLS_IMAGE": func(c *AppConfig) { c.AppDiskToolsImage = "" },
		"APP_EGRESS":           func(c *AppConfig) { c.SingleHostApps.EgressInvalid = "anything" },
		"APP_EDGE_TLS":         func(c *AppConfig) { c.SingleHostApps.EdgeTLS = "" },
	}
	for env, mutate := range cases {
		cfg := singleHostHosting()
		mutate(&cfg)
		err := cfg.Validate()
		if err == nil || !strings.Contains(err.Error(), env) {
			t.Errorf("%s: err = %v, want it named", env, err)
		}
	}
	off := singleHostHosting()
	off.AppHostingEnabled, off.SingleHostApps = false, SingleHostAppsConfig{}
	if err := off.Validate(); err != nil {
		t.Fatalf("with apps off nothing is needed: %v", err)
	}
}

func TestEdgeIssuesLocallyOnlyWithItsOwnCA(t *testing.T) {
	if (SingleHostAppsConfig{EdgeTLS: "admin@example.com"}).EdgeIssuesLocally() {
		t.Fatal("an ACME edge counted as its own CA")
	}
}
