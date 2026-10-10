package config

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"

	"k8s.io/apimachinery/pkg/util/validation"
)

const (
	defaultAppSandboxRuntime = "runsc"
	// appSandboxOptOut runs apps without a sandbox, an operator's explicit choice (ADR 0039).
	appSandboxOptOut      = "none"
	defaultAppProbeBinary = "/usr/local/bin/excalibase-app-probe"
)

// SingleHostAppsConfig is how apps run on a single Docker or Podman host (EXC-575).
type SingleHostAppsConfig struct {
	// SandboxRuntime is the OCI runtime apps run under; empty after APP_SANDBOX_RUNTIME=none.
	SandboxRuntime string
	// Egress gives apps a route to the internet (APP_EGRESS=internet); off by default.
	Egress bool
	// EgressInvalid keeps an APP_EGRESS value that is neither, for Validate to name.
	EgressInvalid               string
	NetworkPrefix, VolumePrefix string
	EdgeContainer               string
	EdgeRoutesDir               string
	EdgeTLSAddress              string
	// EdgeTLS is how the edge gets certificates: "internal" (its own CA) or an ACME account address.
	EdgeTLS     string
	ProbeBinary string
}

// EdgeIssuesLocally: the edge signs app certificates with its own CA.
func (c SingleHostAppsConfig) EdgeIssuesLocally() bool { return c.EdgeTLS == "internal" }

func loadSingleHostApps() SingleHostAppsConfig {
	cfg := SingleHostAppsConfig{
		SandboxRuntime: envOr("APP_SANDBOX_RUNTIME", defaultAppSandboxRuntime),
		NetworkPrefix:  strings.TrimSpace(os.Getenv("APP_NETWORK_PREFIX")),
		VolumePrefix:   strings.TrimSpace(os.Getenv("APP_VOLUME_PREFIX")),
		EdgeContainer:  strings.TrimSpace(os.Getenv("APP_EDGE_CONTAINER")),
		EdgeRoutesDir:  strings.TrimSpace(os.Getenv("APP_EDGE_ROUTES_DIR")),
		EdgeTLSAddress: strings.TrimSpace(os.Getenv("APP_EDGE_TLS_ADDR")),
		EdgeTLS:        strings.TrimSpace(os.Getenv("APP_EDGE_TLS")),
		ProbeBinary:    envOr("APP_PROBE_BINARY", defaultAppProbeBinary),
	}
	if cfg.SandboxRuntime == appSandboxOptOut {
		cfg.SandboxRuntime = ""
	}
	switch egress := envOr("APP_EGRESS", "none"); egress {
	case "none":
	case "internet":
		cfg.Egress = true
	default:
		cfg.EgressInvalid = egress
	}
	return cfg
}

// validateSingleHostApps: APP_DOMAIN and the edge the apps are served through;
// the Kubernetes ingress settings mean nothing here.
func (c AppConfig) validateSingleHostApps() error {
	apps := c.SingleHostApps
	if apps.EgressInvalid != "" {
		return fmt.Errorf("APP_EGRESS %q must be none or internet", apps.EgressInvalid)
	}
	required := []struct{ env, value string }{
		{"APP_DOMAIN", c.AppDomain}, {"APP_NETWORK_PREFIX", apps.NetworkPrefix}, {"APP_VOLUME_PREFIX", apps.VolumePrefix},
		{"APP_EDGE_CONTAINER", apps.EdgeContainer}, {"APP_EDGE_ROUTES_DIR", apps.EdgeRoutesDir},
		{"APP_EDGE_TLS_ADDR", apps.EdgeTLSAddress}, {"APP_EDGE_TLS", apps.EdgeTLS}, {"APP_PROBE_BINARY", apps.ProbeBinary},
		{"APP_DISK_TOOLS_IMAGE", c.AppDiskToolsImage},
	}
	for _, setting := range required {
		if setting.value == "" {
			return fmt.Errorf("APP_HOSTING_ENABLED on a single host needs %s", setting.env)
		}
	}
	if problems := validation.IsDNS1123Subdomain(c.AppDomain); len(problems) > 0 {
		return fmt.Errorf("APP_DOMAIN %q: %s", c.AppDomain, strings.Join(problems, "; "))
	}
	if !filepath.IsAbs(apps.EdgeRoutesDir) {
		return fmt.Errorf("APP_EDGE_ROUTES_DIR %q must be an absolute path", apps.EdgeRoutesDir)
	}
	if _, _, err := net.SplitHostPort(apps.EdgeTLSAddress); err != nil {
		return fmt.Errorf("APP_EDGE_TLS_ADDR %q must be host:port: %w", apps.EdgeTLSAddress, err)
	}
	return nil
}
