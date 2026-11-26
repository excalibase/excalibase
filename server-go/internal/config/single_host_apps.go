package config

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"k8s.io/apimachinery/pkg/util/validation"
)

const (
	defaultAppSandboxRuntime = "runsc"
	// appSandboxOptOut runs apps without a sandbox, an operator's explicit choice (ADR 0039).
	appSandboxOptOut      = "none"
	defaultAppProbeBinary = "/usr/local/bin/excalibase-app-probe"
	defaultAppUserns      = "auto"
	// defaultAppTmpPercent is the share of an app's memory limit its /tmp may fill (EXC-732).
	defaultAppTmpPercent = 25
	// defaultAppLogMaxSize and defaultAppLogMaxFiles cap one app container's log at 15 MiB (EXC-732).
	defaultAppLogMaxSize  = "5m"
	defaultAppLogMaxFiles = 3
)

// appLogSize is the engine's log size: digits and k, m or g.
var appLogSize = regexp.MustCompile(`^[1-9][0-9]*[kmg]$`)

// SingleHostAppsConfig is how apps run on a single Docker or Podman host (EXC-575).
type SingleHostAppsConfig struct {
	// SandboxRuntime is the OCI runtime apps run under; empty after APP_SANDBOX_RUNTIME=none.
	SandboxRuntime string
	// Egress gives apps a route to the internet (APP_EGRESS=internet); off by default.
	Egress bool
	// AcceptUnguardedEgress is APP_EGRESS_UNGUARDED=accept: egress where no firewall can guard it (rootless Podman).
	AcceptUnguardedEgress bool
	// EgressInvalid keeps an APP_EGRESS value that is neither, for Validate to name.
	EgressInvalid string
	// Userns is APP_USERNS (auto, per-app or shared): whether unsandboxed apps on rootless
	// Podman each get their own user range (EXC-682). Validate refuses any other value.
	Userns string
	// TmpPercent is APP_TMP_PERCENT: the size of an app's /tmp tmpfs as a percentage of its
	// memory limit. The root filesystem is read-only, so /tmp and the app's disk are what it
	// may write. -1 keeps a value that is not a whole number, for Validate to refuse.
	TmpPercent int
	// LogMaxSize and LogMaxFiles are APP_LOG_MAX_SIZE and APP_LOG_MAX_FILES: each app container's log
	// is rotated at that size and keeps that many files. LogMaxFiles is -1 for a value that is not a
	// whole number, for Validate to refuse.
	LogMaxSize                  string
	LogMaxFiles                 int
	NetworkPrefix, VolumePrefix string
	EdgeContainer               string
	EdgeRoutesDir               string
	EdgeTLSAddress              string
	// EdgeTLS is how the edge gets certificates: "internal" (its own CA) or an ACME account address.
	EdgeTLS string
	// EdgeCAURL is where the edge serves its own CA's root, for apps and probes to trust (EXC-608).
	EdgeCAURL string
	// EdgeAliases are the platform's hostnames the edge answers to on project networks (EXC-608);
	// the engine proxy admits exactly these.
	EdgeAliases []string
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
		EdgeCAURL:      strings.TrimSpace(os.Getenv("APP_EDGE_CA_URL")),
		EdgeAliases:    splitTrimmed(os.Getenv("APP_EDGE_ALIASES")),
		ProbeBinary:    envOr("APP_PROBE_BINARY", defaultAppProbeBinary),
		Userns:         envOr("APP_USERNS", defaultAppUserns),
	}
	if cfg.SandboxRuntime == appSandboxOptOut {
		cfg.SandboxRuntime = ""
	}
	cfg.TmpPercent = defaultAppTmpPercent
	if raw := strings.TrimSpace(os.Getenv("APP_TMP_PERCENT")); raw != "" {
		percent, err := strconv.Atoi(raw)
		if err != nil {
			percent = -1
		}
		cfg.TmpPercent = percent
	}
	cfg.LogMaxSize = envOr("APP_LOG_MAX_SIZE", defaultAppLogMaxSize)
	cfg.LogMaxFiles = defaultAppLogMaxFiles
	if raw := strings.TrimSpace(os.Getenv("APP_LOG_MAX_FILES")); raw != "" {
		files, err := strconv.Atoi(raw)
		if err != nil {
			files = -1
		}
		cfg.LogMaxFiles = files
	}
	cfg.AcceptUnguardedEgress = os.Getenv("APP_EGRESS_UNGUARDED") == "accept"
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
	switch apps.Userns {
	case "", "auto", "per-app", "shared":
	default:
		return fmt.Errorf("APP_USERNS %q must be auto, per-app or shared", apps.Userns)
	}
	if apps.TmpPercent < 1 || apps.TmpPercent > 100 {
		return errors.New("APP_TMP_PERCENT must be a whole number from 1 to 100")
	}
	if !appLogSize.MatchString(apps.LogMaxSize) {
		return fmt.Errorf("APP_LOG_MAX_SIZE %q must be a whole number and k, m or g, such as 5m", apps.LogMaxSize)
	}
	if apps.LogMaxFiles < 1 || apps.LogMaxFiles > 100 {
		return errors.New("APP_LOG_MAX_FILES must be a whole number from 1 to 100")
	}
	required := []struct{ env, value string }{
		{"APP_DOMAIN", c.AppDomain}, {"APP_NETWORK_PREFIX", apps.NetworkPrefix}, {"APP_VOLUME_PREFIX", apps.VolumePrefix},
		{"APP_EDGE_CONTAINER", apps.EdgeContainer}, {"APP_EDGE_ROUTES_DIR", apps.EdgeRoutesDir},
		{"APP_EDGE_TLS_ADDR", apps.EdgeTLSAddress}, {"APP_EDGE_TLS", apps.EdgeTLS}, {"APP_PROBE_BINARY", apps.ProbeBinary},
		{"APP_DISK_TOOLS_IMAGE", c.AppDiskToolsImage},
	}
	if apps.EdgeIssuesLocally() {
		required = append(required, struct{ env, value string }{"APP_EDGE_CA_URL", apps.EdgeCAURL})
	}
	for _, setting := range required {
		if setting.value == "" {
			return fmt.Errorf("APP_HOSTING_ENABLED on a single host needs %s", setting.env)
		}
	}
	for _, alias := range apps.EdgeAliases {
		if problems := validation.IsDNS1123Subdomain(alias); len(problems) > 0 {
			return fmt.Errorf("APP_EDGE_ALIASES %q: %s", alias, strings.Join(problems, "; "))
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

// splitTrimmed reads a comma-separated list, dropping blanks.
func splitTrimmed(raw string) []string {
	var items []string
	for _, item := range strings.Split(raw, ",") {
		if item = strings.TrimSpace(item); item != "" {
			items = append(items, item)
		}
	}
	return items
}
