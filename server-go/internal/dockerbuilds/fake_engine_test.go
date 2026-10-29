package dockerbuilds

import (
	"archive/tar"
	"bytes"
	"context"
	"fmt"
	"io"
	"strings"
	"sync"

	"github.com/containerd/errdefs"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/api/types/system"
	"github.com/docker/docker/api/types/volume"
	"github.com/docker/docker/pkg/stdcopy"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

type fakeContainer struct {
	name      string
	config    container.Config
	host      container.HostConfig
	networks  []string
	state     string
	exitCode  int
	oomKilled bool
	files     map[string][]byte
	logs      string
}

// fakeEngine is an in-memory engine with just enough behaviour for the runner's decisions.
type fakeEngine struct {
	mu         sync.Mutex
	containers map[string]*fakeContainer
	networks   map[string]network.CreateOptions
	volumes    map[string]bool
	pulled     []string
	runtimes   []string
	calls      []string
	// ignoresGatewayMode is an engine older than Docker 28, or Podman.
	ignoresGatewayMode bool
	// pullMessage is an error the engine streams back on a pull; failCreate refuses a create of that name.
	pullMessage, failCreate, failStart string
	// networkOptionsSeen are the driver options of every network created.
	networkOptionsSeen []map[string]string
}

func newFakeEngine() *fakeEngine {
	return &fakeEngine{containers: map[string]*fakeContainer{}, networks: map[string]network.CreateOptions{},
		volumes: map[string]bool{}, runtimes: []string{"runc", "runsc"}}
}

func (f *fakeEngine) record(format string, args ...any) {
	f.calls = append(f.calls, fmt.Sprintf(format, args...))
}

func (f *fakeEngine) get(name string) *fakeContainer {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.containers[name]
}

// finish ends a step as the engine would see it.
func (f *fakeEngine) finish(name string, code int, oom bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	c := f.containers[name]
	c.state, c.exitCode, c.oomKilled = "exited", code, oom
}

func (f *fakeEngine) Info(context.Context) (system.Info, error) {
	runtimes := map[string]system.RuntimeWithStatus{}
	for _, name := range f.runtimes {
		runtimes[name] = system.RuntimeWithStatus{}
	}
	return system.Info{Runtimes: runtimes}, nil
}

func (f *fakeEngine) ContainerCreate(_ context.Context, config *container.Config, host *container.HostConfig,
	networking *network.NetworkingConfig, _ *ocispec.Platform, name string) (container.CreateResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if name == f.failCreate {
		return container.CreateResponse{}, errdefs.ErrInvalidArgument.WithMessage("refused: " + name)
	}
	if _, taken := f.containers[name]; taken {
		return container.CreateResponse{}, errdefs.ErrConflict.WithMessage("name taken: " + name)
	}
	c := &fakeContainer{name: name, config: *config, host: *host, state: "created", files: map[string][]byte{}}
	if mode := string(host.NetworkMode); mode != "" && mode != "none" {
		c.networks = append(c.networks, mode)
	}
	if networking != nil {
		for name := range networking.EndpointsConfig {
			c.networks = append(c.networks, name)
		}
	}
	f.containers[name] = c
	f.record("create %s", name)
	return container.CreateResponse{ID: name}, nil
}

func (f *fakeEngine) ContainerStart(_ context.Context, id string, _ container.StartOptions) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	c, ok := f.containers[id]
	if !ok {
		return errdefs.ErrNotFound.WithMessage(id)
	}
	if id == f.failStart {
		return errdefs.ErrInvalidArgument.WithMessage("unknown runtime")
	}
	c.state = "running"
	f.record("start %s", id)
	return nil
}

func (f *fakeEngine) ContainerStop(_ context.Context, id string, _ container.StopOptions) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if c, ok := f.containers[id]; ok && c.state == "running" {
		c.state, c.exitCode = "exited", 137
	}
	f.record("stop %s", id)
	return nil
}

func (f *fakeEngine) ContainerRemove(_ context.Context, id string, _ container.RemoveOptions) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.containers[id]; !ok {
		return errdefs.ErrNotFound.WithMessage(id)
	}
	delete(f.containers, id)
	f.record("remove %s", id)
	return nil
}

func (f *fakeEngine) ContainerInspect(_ context.Context, id string) (container.InspectResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	c, ok := f.containers[id]
	if !ok {
		return container.InspectResponse{}, errdefs.ErrNotFound.WithMessage(id)
	}
	config := c.config
	networks := map[string]*network.EndpointSettings{}
	for i, name := range c.networks {
		networks[name] = &network.EndpointSettings{IPAddress: fmt.Sprintf("10.250.0.%d", i+2)}
	}
	return container.InspectResponse{
		ContainerJSONBase: &container.ContainerJSONBase{Name: "/" + c.name, HostConfig: &c.host, State: &container.State{
			Status: c.state, Running: c.state == "running", ExitCode: c.exitCode, OOMKilled: c.oomKilled}},
		Config: &config, NetworkSettings: &container.NetworkSettings{Networks: networks},
	}, nil
}

func (f *fakeEngine) ContainerLogs(_ context.Context, id string, _ container.LogsOptions) (io.ReadCloser, error) {
	c := f.get(id)
	if c == nil {
		return nil, errdefs.ErrNotFound.WithMessage(id)
	}
	var out bytes.Buffer
	_, _ = stdcopy.NewStdWriter(&out, stdcopy.Stdout).Write([]byte(c.logs))
	return io.NopCloser(&out), nil
}

func (f *fakeEngine) ContainerStatPath(_ context.Context, id, path string) (container.PathStat, error) {
	c := f.get(id)
	if c == nil {
		return container.PathStat{}, errdefs.ErrNotFound.WithMessage(id)
	}
	if _, ok := c.files[path]; !ok {
		return container.PathStat{}, errdefs.ErrNotFound.WithMessage(path)
	}
	return container.PathStat{Name: path}, nil
}

func (f *fakeEngine) CopyFromContainer(_ context.Context, id, path string) (io.ReadCloser, container.PathStat, error) {
	c := f.get(id)
	if c == nil {
		return nil, container.PathStat{}, errdefs.ErrNotFound.WithMessage(id)
	}
	body, ok := c.files[path]
	if !ok {
		return nil, container.PathStat{}, errdefs.ErrNotFound.WithMessage(path)
	}
	var archive bytes.Buffer
	writer := tar.NewWriter(&archive)
	_ = writer.WriteHeader(&tar.Header{Name: "site.tar.gz", Mode: 0o644, Size: int64(len(body))})
	_, _ = writer.Write(body)
	_ = writer.Close()
	return io.NopCloser(&archive), container.PathStat{Name: "site.tar.gz", Size: int64(len(body))}, nil
}

func (f *fakeEngine) ImagePull(_ context.Context, ref string, _ image.PullOptions) (io.ReadCloser, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pulled = append(f.pulled, ref)
	if f.pullMessage != "" {
		return io.NopCloser(strings.NewReader(`{"status":"pulling"}{"errorDetail":{"message":"` + f.pullMessage + `"}}`)), nil
	}
	return io.NopCloser(strings.NewReader(`{"status":"done"}`)), nil
}

func (f *fakeEngine) NetworkCreate(_ context.Context, name string, options network.CreateOptions) (network.CreateResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.networks[name]; ok {
		return network.CreateResponse{}, errdefs.ErrConflict.WithMessage(name)
	}
	f.networks[name] = options
	f.networkOptionsSeen = append(f.networkOptionsSeen, options.Options)
	f.record("network create %s", name)
	return network.CreateResponse{ID: name}, nil
}

func (f *fakeEngine) NetworkInspect(_ context.Context, name string, _ network.InspectOptions) (network.Inspect, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	options, ok := f.networks[name]
	if !ok {
		return network.Inspect{}, errdefs.ErrNotFound.WithMessage(name)
	}
	inspect := network.Inspect{Name: name, Internal: options.Internal, Labels: options.Labels}
	// A bridge gets an address on the host unless the engine honours the isolated gateway mode.
	if options.Options["com.docker.network.bridge.gateway_mode_ipv4"] != "isolated" || f.ignoresGatewayMode {
		inspect.IPAM.Config = []network.IPAMConfig{{Subnet: "172.30.0.0/16", Gateway: "172.30.0.1"}}
	} else {
		inspect.IPAM.Config = []network.IPAMConfig{{Subnet: "172.30.0.0/16"}}
	}
	return inspect, nil
}

func (f *fakeEngine) NetworkRemove(_ context.Context, name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.networks[name]; !ok {
		return errdefs.ErrNotFound.WithMessage(name)
	}
	delete(f.networks, name)
	f.record("network remove %s", name)
	return nil
}

func (f *fakeEngine) NetworkConnect(_ context.Context, name, id string, _ *network.EndpointSettings) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	c, ok := f.containers[id]
	if !ok {
		return errdefs.ErrNotFound.WithMessage(id)
	}
	c.networks = append(c.networks, name)
	f.record("connect %s %s", id, name)
	return nil
}

func (f *fakeEngine) VolumeCreate(_ context.Context, options volume.CreateOptions) (volume.Volume, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.volumes[options.Name] = true
	f.record("volume create %s", options.Name)
	return volume.Volume{Name: options.Name}, nil
}

func (f *fakeEngine) VolumeRemove(_ context.Context, name string, _ bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.volumes[name] {
		return errdefs.ErrNotFound.WithMessage(name)
	}
	delete(f.volumes, name)
	f.record("volume remove %s", name)
	return nil
}
