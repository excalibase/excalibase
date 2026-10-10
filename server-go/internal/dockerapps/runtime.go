package dockerapps

import (
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/excalibase/provisioning-poc/internal/apphost"
	"github.com/excalibase/provisioning-poc/internal/k8s"
)

const (
	// appPidsLimit stops a fork bomb from taking the host's process table.
	appPidsLimit = int64(1024)
	// managedPrefix is what the engine proxy requires of every name it creates.
	managedPrefix = "excalibase-"
	// defaultMinReady is Kubernetes' minReadySeconds for apps.
	defaultMinReady     = 10 * time.Second
	defaultPollInterval = 2 * time.Second
	// defaultCrashRestarts fails a rollout once a container has restarted this often (the crash-loop limit on Kubernetes).
	defaultCrashRestarts = 3
	appStopSeconds       = 30
	toolCheckTimeout     = time.Minute
)

// ProjectScopes maps a project to its scope ("namespace"): on a single host
// that is the id of the project's database container.
type ProjectScopes interface {
	ProjectOf(namespace string) (string, error)
	NamespaceOf(projectID string) (string, error)
}

// Options are the operator's single-host settings; New refuses any missing one.
type Options struct {
	// NetworkPrefix and VolumePrefix are the engine proxy's PROXY_NETWORK_PREFIX and PROXY_VOLUME_PREFIX.
	NetworkPrefix, VolumePrefix string
	// EdgeContainer is the edge's container name (the proxy's PROXY_EDGE_CONTAINER);
	// EdgeTLSAddress is where provisioning reaches its HTTPS listener.
	EdgeContainer, EdgeTLSAddress string
	// EdgeIssuesLocally: the edge signs with its own CA (EXCALIBASE_TLS=internal), which
	// cannot fail; EdgeRoots verifies what it serves otherwise (nil: the system's roots).
	EdgeIssuesLocally bool
	EdgeRoots         *x509.CertPool
	// RoutesDir holds the edge's route files and each app's runtime record.
	RoutesDir string
	// SandboxRuntime is the OCI runtime apps run under; empty is the operator's opt-out.
	SandboxRuntime string
	// Egress gives project networks a gateway; Isolate (Podman) keeps such a bridge off the others.
	Egress, Isolate bool
	ProbeBinary     []byte
	ToolsImage      string
	Route           apphost.Route
	ReservedHosts   []string
	Projects        ProjectScopes
	// MinReady is how long a container must stay ready to count (DefaultMinReady in production).
	// PollInterval and CrashRestarts tune the rollout watch; zero takes the defaults.
	PollInterval, MinReady time.Duration
	CrashRestarts          int
}

// Runtime is the app runtime of a single Docker or Podman host.
type Runtime struct {
	engine Engine
	opts   Options
	// mu serialises the per-app records and route files.
	mu sync.Mutex
}

func New(engine Engine, opts Options) (*Runtime, error) {
	if engine == nil {
		return nil, errors.New("app hosting on a single host needs the engine")
	}
	if err := opts.validate(); err != nil {
		return nil, err
	}
	if opts.PollInterval <= 0 {
		opts.PollInterval = defaultPollInterval
	}
	if opts.CrashRestarts <= 0 {
		opts.CrashRestarts = defaultCrashRestarts
	}
	return &Runtime{engine: engine, opts: opts}, nil
}

// DefaultMinReady is what the wiring passes for MinReady.
const DefaultMinReady = defaultMinReady

func (o Options) validate() error {
	for setting, prefix := range map[string]string{"network prefix": o.NetworkPrefix, "volume prefix": o.VolumePrefix} {
		if !strings.HasPrefix(prefix, managedPrefix) || len(prefix) <= len(managedPrefix) || !strings.HasSuffix(prefix, "-") ||
			strings.HasPrefix(prefix, managedPrefix+"platform") {
			return fmt.Errorf("the %s %q must extend %q with a segment ending in '-'", setting, prefix, managedPrefix)
		}
	}
	required := map[string]string{
		"edge container": o.EdgeContainer, "edge TLS address": o.EdgeTLSAddress, "routes directory": o.RoutesDir,
		"disk tools image": o.ToolsImage, "app domain": o.Route.Domain,
	}
	for setting, value := range required {
		if value == "" {
			return fmt.Errorf("app hosting on a single host needs the %s", setting)
		}
	}
	if len(o.ProbeBinary) == 0 {
		return errors.New("app hosting on a single host needs the readiness probe binary")
	}
	if o.Projects == nil {
		return errors.New("app hosting on a single host needs the project lookup")
	}
	return checkWritable(o.RoutesDir)
}

func checkWritable(dir string) error {
	probe, err := os.CreateTemp(dir, ".write-check-")
	if err != nil {
		return fmt.Errorf("the routes directory %s is not writable: %w", dir, err)
	}
	name := probe.Name()
	if err := errors.Join(probe.Close(), os.Remove(name)); err != nil {
		return fmt.Errorf("the routes directory %s: %w", dir, err)
	}
	return nil
}

// VerifyEngine refuses an engine without the sandbox runtime apps are configured to run under.
func (r *Runtime) VerifyEngine(ctx context.Context) error {
	info, err := r.engine.Info(ctx)
	if err != nil {
		return fmt.Errorf("read the engine's runtimes: %w", err)
	}
	if _, ok := info.Runtimes[r.opts.SandboxRuntime]; r.opts.SandboxRuntime != "" && !ok {
		names := make([]string, 0, len(info.Runtimes))
		for name := range info.Runtimes {
			names = append(names, name)
		}
		return fmt.Errorf("the engine has no %q runtime (it has %s): install gVisor, or set APP_SANDBOX_RUNTIME=none to run apps without a sandbox",
			r.opts.SandboxRuntime, strings.Join(names, ", "))
	}
	if _, err := r.runTool(ctx, k8s.DiskJobOptions{Timeout: toolCheckTimeout}, []string{"true"}); err != nil {
		return fmt.Errorf("the disk tools image %s does not run here (APP_DISK_TOOLS_IMAGE): %w", r.opts.ToolsImage, err)
	}
	return nil
}

// IsPodman tells the wiring whether the engine is Podman, whose bridges need the isolate option.
func IsPodman(ctx context.Context, engine Engine) (bool, error) {
	version, err := engine.ServerVersion(ctx)
	if err != nil {
		return false, fmt.Errorf("read the engine's version: %w", err)
	}
	for _, component := range version.Components {
		if strings.Contains(strings.ToLower(component.Name), "podman") {
			return true, nil
		}
	}
	return false, nil
}
