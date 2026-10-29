// Package dockerbuilds runs git builds of sites on a single Docker or Podman
// host (EXC-671), as the build Job does on Kubernetes (EXC-354): the same
// scripts, one container per step, every call through the engine proxy.
//
// A build is a work volume, an internal network with no route anywhere, and
// five containers: the egress sidecar (our forward proxy, the build's only way
// out), then prepare → clone → build → collect, each started when the one
// before it exits 0. Only clone holds the git token, only build reads the
// declared variables, and no step holds a platform credential.
package dockerbuilds

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/api/types/system"
	"github.com/docker/docker/api/types/volume"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"

	"github.com/excalibase/provisioning-poc/internal/buildegress"
	"github.com/excalibase/provisioning-poc/internal/sitebuilds"
)

// Engine is the slice of the Docker API the runner calls, every call one the
// engine proxy admits; the Docker SDK client satisfies it.
type Engine interface {
	Info(ctx context.Context) (system.Info, error)
	ContainerCreate(ctx context.Context, config *container.Config, hostConfig *container.HostConfig,
		networkingConfig *network.NetworkingConfig, platform *ocispec.Platform, containerName string) (container.CreateResponse, error)
	ContainerStart(ctx context.Context, containerID string, options container.StartOptions) error
	ContainerStop(ctx context.Context, containerID string, options container.StopOptions) error
	ContainerRemove(ctx context.Context, containerID string, options container.RemoveOptions) error
	ContainerInspect(ctx context.Context, containerID string) (container.InspectResponse, error)
	ContainerLogs(ctx context.Context, containerID string, options container.LogsOptions) (io.ReadCloser, error)
	ContainerStatPath(ctx context.Context, containerID, path string) (container.PathStat, error)
	CopyFromContainer(ctx context.Context, containerID, srcPath string) (io.ReadCloser, container.PathStat, error)
	ImagePull(ctx context.Context, ref string, options image.PullOptions) (io.ReadCloser, error)
	NetworkCreate(ctx context.Context, name string, options network.CreateOptions) (network.CreateResponse, error)
	NetworkInspect(ctx context.Context, networkID string, options network.InspectOptions) (network.Inspect, error)
	NetworkRemove(ctx context.Context, networkID string) error
	NetworkConnect(ctx context.Context, networkID, containerID string, config *network.EndpointSettings) error
	VolumeCreate(ctx context.Context, options volume.CreateOptions) (volume.Volume, error)
	VolumeRemove(ctx context.Context, volumeID string, force bool) error
}

const (
	// managedPrefix is what the engine proxy requires of every name it creates.
	managedPrefix = "excalibase-"
	// egressNetworkName is the one network with a route out, shared by every build's sidecar.
	egressNetworkName = "build-egress"
	// gatewayModeIPv4 is Docker's bridge option; "isolated" gives an internal network no address on the host.
	gatewayModeIPv4 = "com.docker.network.bridge.gateway_mode_ipv4"
	// defaultDiskCheckEvery paces the disk watcher; a build may overshoot its disk by this much writing.
	defaultDiskCheckEvery = 10 * time.Second
	// defaultHostDiskFloor stops a build when the host has less free disk than this, whoever used it.
	defaultHostDiskFloor = int64(2) << 30
)

// buildIDPattern keeps a build id usable inside container, network and volume names.
var buildIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)

// Options are the operator's single-host settings; NewRunner refuses any missing one.
type Options struct {
	// NetworkPrefix and VolumePrefix are the engine proxy's PROXY_NETWORK_PREFIX and PROXY_VOLUME_PREFIX.
	NetworkPrefix, VolumePrefix string
	// SandboxRuntime is the OCI runtime build steps run under; empty is the operator's opt-out (APP_SANDBOX_RUNTIME=none).
	SandboxRuntime string
	// EgressImage holds /bin/excalibase-build-egress: provisioning's own image, never pulled.
	EgressImage string
	// PrepareImage runs only the work volume's chmod, as root; minimal and pinned.
	PrepareImage string
	// ExtraDenyCIDRs are denied beyond the private ranges; EgressAllow are host:port entries admitted past them.
	ExtraDenyCIDRs, EgressAllow []string
	// Isolate (Podman) keeps the egress bridge from routing to the other bridges.
	Isolate bool
	// DiskCheckEvery paces the disk watcher; zero is 10 s.
	DiskCheckEvery time.Duration
	// HostDiskFloor is the free disk, in bytes, below which builds stop; zero is 2 GiB.
	HostDiskFloor int64
	// Now is the clock; nil is time.Now.
	Now func() time.Time
}

func (o Options) validate() error {
	for setting, prefix := range map[string]string{"network prefix": o.NetworkPrefix, "volume prefix": o.VolumePrefix} {
		if !strings.HasPrefix(prefix, managedPrefix) || len(prefix) <= len(managedPrefix) || !strings.HasSuffix(prefix, "-") {
			return fmt.Errorf("site builds on a single host: the %s %q must extend %q with a segment ending in '-'", setting, prefix, managedPrefix)
		}
	}
	if strings.TrimSpace(o.EgressImage) == "" {
		return errors.New("site builds on a single host need the egress image (SITES_BUILD_EGRESS_IMAGE)")
	}
	if strings.TrimSpace(o.PrepareImage) == "" {
		return errors.New("site builds on a single host need the prepare image (SITES_BUILD_PREPARE_IMAGE)")
	}
	if _, err := buildegress.NewPolicy(o.ExtraDenyCIDRs, o.EgressAllow); err != nil {
		return fmt.Errorf("site builds on a single host: %w", err)
	}
	return nil
}

// Runner runs site builds on one Docker or Podman host.
type Runner struct {
	engine  Engine
	opts    Options
	mu      sync.Mutex
	scripts map[string]string
	// hostIsolated is what Verify found: build networks have no address on the host.
	hostIsolated bool
}

var _ sitebuilds.Runner = (*Runner)(nil)

func NewRunner(engine Engine, opts Options) (*Runner, error) {
	if engine == nil {
		return nil, errors.New("site builds on a single host need the engine")
	}
	if err := opts.validate(); err != nil {
		return nil, err
	}
	if opts.DiskCheckEvery <= 0 {
		opts.DiskCheckEvery = defaultDiskCheckEvery
	}
	if opts.HostDiskFloor <= 0 {
		opts.HostDiskFloor = defaultHostDiskFloor
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	scripts, err := loadScripts()
	if err != nil {
		return nil, err
	}
	return &Runner{engine: engine, opts: opts, scripts: scripts}, nil
}

// Verify refuses an engine without the sandbox runtime builds are configured to
// run under, and finds whether a build network keeps builds off the host.
func (r *Runner) Verify(ctx context.Context) error {
	if r.opts.SandboxRuntime != "" {
		info, err := r.engine.Info(ctx)
		if err != nil {
			return fmt.Errorf("read the engine's runtimes: %w", err)
		}
		if _, ok := info.Runtimes[r.opts.SandboxRuntime]; !ok {
			return fmt.Errorf("the engine has no %q runtime, which site builds run under: install gVisor, "+
				"or set APP_SANDBOX_RUNTIME=none to run builds without a sandbox (trusted code only)", r.opts.SandboxRuntime)
		}
	}
	isolated, err := r.checkHostIsolation(ctx)
	if err != nil {
		return err
	}
	r.mu.Lock()
	r.hostIsolated = isolated
	r.mu.Unlock()
	return nil
}

// checkHostIsolation makes a build network and reads whether its bridge has an address on the host:
// a container can reach the host's own services through that address unless the host firewall drops it.
func (r *Runner) checkHostIsolation(ctx context.Context) (bool, error) {
	name := r.opts.NetworkPrefix + "build-check-" + strconv.FormatInt(r.opts.Now().UnixNano(), 36)
	if _, err := r.engine.NetworkCreate(ctx, name, r.buildNetworkOptions(map[string]string{labelManaged: "true", labelComponent: componentBuild})); err != nil {
		return false, fmt.Errorf("create a network to check build isolation: %w", err)
	}
	defer func() {
		if err := r.engine.NetworkRemove(context.WithoutCancel(ctx), name); err != nil {
			log.Printf("site builds: remove the isolation check network %s: %v", name, err)
		}
	}()
	inspect, err := r.engine.NetworkInspect(ctx, name, network.InspectOptions{})
	if err != nil {
		return false, fmt.Errorf("inspect the isolation check network: %w", err)
	}
	for _, config := range inspect.IPAM.Config {
		if config.Gateway != "" {
			return false, nil
		}
	}
	return true, nil
}

// HostIsolated is whether build networks have no address on the host (Docker 28+); false means
// the host firewall must keep builds off the host's own services. Valid after Verify.
func (r *Runner) HostIsolated() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.hostIsolated
}

// buildNetworkOptions: internal, and on Docker with no address on the host (gateway mode isolated, Docker 28+).
func (r *Runner) buildNetworkOptions(labels map[string]string) network.CreateOptions {
	options := network.CreateOptions{Driver: "bridge", Internal: true, Labels: labels}
	if !r.opts.Isolate {
		options.Options = map[string]string{gatewayModeIPv4: "isolated"}
	}
	return options
}

func (r *Runner) buildNetwork(buildID string) string {
	return r.opts.NetworkPrefix + "build-" + buildID
}
func (r *Runner) egressNetwork() string            { return r.opts.NetworkPrefix + egressNetworkName }
func (r *Runner) workVolume(buildID string) string { return r.opts.VolumePrefix + "build-" + buildID }
func containerName(buildID string, step string) string {
	return managedPrefix + "build-" + buildID + "-" + step
}
