package dockerapps

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/containerd/errdefs"
	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/api/types/system"
	"github.com/docker/docker/api/types/volume"
	"github.com/docker/docker/pkg/stdcopy"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

type fakeContainer struct {
	id, name string
	config   container.Config
	host     container.HostConfig
	networks []string
	state    string
	restarts int
	created  time.Time
	files    map[string][]byte
	logs     string
}

type fakeNetwork struct {
	options network.CreateOptions
	members []string // container ids
}

// fakeEngine is an in-memory engine: containers, networks, volumes and images
// with just enough behaviour for the runtime's decisions.
type fakeEngine struct {
	mu         sync.Mutex
	seq        int
	containers map[string]*fakeContainer
	networks   map[string]*fakeNetwork
	volumes    map[string]map[string]string
	pulled     []string
	pullAuth   map[string]string
	pullErr    map[string]string
	runtimes   []string
	podman     bool
	// probeOK decides an exec of the probe; nil passes every probe.
	probeOK func(c *fakeContainer) bool
	// toolOutput is what a tool container prints for its command.
	toolOutput func(cmd []string) (string, int)
	execs      map[string]string
	calls      []string
}

func newFakeEngine() *fakeEngine {
	return &fakeEngine{
		containers: map[string]*fakeContainer{}, networks: map[string]*fakeNetwork{},
		volumes: map[string]map[string]string{}, pullAuth: map[string]string{}, pullErr: map[string]string{},
		runtimes: []string{"runc", "runsc"}, execs: map[string]string{},
	}
}

func (f *fakeEngine) record(format string, args ...any) {
	f.calls = append(f.calls, fmt.Sprintf(format, args...))
}

func (f *fakeEngine) find(ref string) (*fakeContainer, error) {
	if c, ok := f.containers[ref]; ok {
		return c, nil
	}
	for _, c := range f.containers {
		if c.name == ref {
			return c, nil
		}
	}
	return nil, errdefs.ErrNotFound.WithMessage("no such container " + ref)
}

// addContainer registers a container the runtime did not create, such as a database or the edge.
func (f *fakeEngine) addContainer(id, name string, labels map[string]string) {
	f.containers[id] = &fakeContainer{id: id, name: name, config: container.Config{Labels: labels}, state: "running",
		created: time.Now(), files: map[string][]byte{}}
}

func (f *fakeEngine) Info(context.Context) (system.Info, error) {
	runtimes := map[string]system.RuntimeWithStatus{}
	for _, name := range f.runtimes {
		runtimes[name] = system.RuntimeWithStatus{}
	}
	return system.Info{NCPU: 8, MemTotal: 16 << 30, Name: "host", Runtimes: runtimes}, nil
}

func (f *fakeEngine) ServerVersion(context.Context) (types.Version, error) {
	if f.podman {
		return types.Version{Components: []types.ComponentVersion{{Name: "Podman Engine", Version: "5.8.1"}}}, nil
	}
	return types.Version{Components: []types.ComponentVersion{{Name: "Engine", Version: "29.0"}}}, nil
}

func (f *fakeEngine) ContainerList(_ context.Context, options container.ListOptions) ([]container.Summary, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []container.Summary
	for _, c := range f.containers {
		if !options.All && c.state != "running" && c.state != "restarting" {
			continue
		}
		if !matchesLabels(c.config.Labels, options.Filters.Get("label")) {
			continue
		}
		out = append(out, container.Summary{ID: c.id, Names: []string{"/" + c.name}, Labels: c.config.Labels,
			State: c.state, Created: c.created.Unix(), Image: c.config.Image})
	}
	slices.SortFunc(out, func(a, b container.Summary) int { return strings.Compare(a.ID, b.ID) })
	return out, nil
}

func matchesLabels(labels map[string]string, wanted []string) bool {
	for _, want := range wanted {
		key, value, hasValue := strings.Cut(want, "=")
		got, ok := labels[key]
		if !ok || (hasValue && got != value) {
			return false
		}
	}
	return true
}

func (f *fakeEngine) ContainerCreate(_ context.Context, config *container.Config, host *container.HostConfig,
	netCfg *network.NetworkingConfig, _ *ocispec.Platform, name string) (container.CreateResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, err := f.find(name); err == nil {
		return container.CreateResponse{}, errdefs.ErrConflict.WithMessage("name in use " + name)
	}
	if !slices.Contains(f.pulled, config.Image) && !strings.HasPrefix(config.Image, "tools") {
		return container.CreateResponse{}, errdefs.ErrNotFound.WithMessage("no such image " + config.Image)
	}
	f.seq++
	id := fmt.Sprintf("c%04d", f.seq)
	c := &fakeContainer{id: id, name: name, config: *config, host: *host, state: "created",
		created: time.Now().Add(time.Duration(f.seq) * time.Millisecond), files: map[string][]byte{}}
	for net := range netCfg.EndpointsConfig {
		if _, ok := f.networks[net]; !ok {
			return container.CreateResponse{}, errdefs.ErrNotFound.WithMessage("no such network " + net)
		}
		c.networks = append(c.networks, net)
		f.networks[net].members = append(f.networks[net].members, id)
	}
	f.containers[id] = c
	f.record("create %s", name)
	return container.CreateResponse{ID: id}, nil
}

func (f *fakeEngine) ContainerStart(_ context.Context, id string, _ container.StartOptions) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	c, err := f.find(id)
	if err != nil {
		return err
	}
	c.state = "running"
	f.record("start %s", c.name)
	return nil
}

func (f *fakeEngine) ContainerStop(_ context.Context, id string, _ container.StopOptions) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	c, err := f.find(id)
	if err != nil {
		return err
	}
	c.state = "exited"
	f.record("stop %s", c.name)
	return nil
}

func (f *fakeEngine) ContainerRemove(_ context.Context, id string, _ container.RemoveOptions) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	c, err := f.find(id)
	if err != nil {
		return err
	}
	for _, net := range f.networks {
		net.members = slices.DeleteFunc(net.members, func(m string) bool { return m == c.id })
	}
	delete(f.containers, c.id)
	f.record("remove %s", c.name)
	return nil
}

func (f *fakeEngine) ContainerInspect(_ context.Context, id string) (container.InspectResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	c, err := f.find(id)
	if err != nil {
		return container.InspectResponse{}, err
	}
	config, host := c.config, c.host
	networks := map[string]*network.EndpointSettings{}
	for _, net := range c.networks {
		networks[net] = &network.EndpointSettings{}
	}
	return container.InspectResponse{
		ContainerJSONBase: &container.ContainerJSONBase{ID: c.id, Name: "/" + c.name, RestartCount: c.restarts, HostConfig: &host,
			State: &container.State{Status: c.state, Running: c.state == "running", Restarting: c.state == "restarting"}},
		Config: &config, NetworkSettings: &container.NetworkSettings{Networks: networks},
	}, nil
}

func (f *fakeEngine) ContainerLogs(_ context.Context, id string, options container.LogsOptions) (io.ReadCloser, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	c, err := f.find(id)
	if err != nil {
		return nil, err
	}
	var framed bytes.Buffer
	writer := stdcopy.NewStdWriter(&framed, stdcopy.Stdout)
	_, _ = writer.Write([]byte(c.logs))
	f.record("logs %s since=%s tail=%s", c.name, options.Since, options.Tail)
	return io.NopCloser(&framed), nil
}

func (f *fakeEngine) ContainerWait(_ context.Context, id string, _ container.WaitCondition) (<-chan container.WaitResponse, <-chan error) {
	results, errs := make(chan container.WaitResponse, 1), make(chan error, 1)
	f.mu.Lock()
	defer f.mu.Unlock()
	c, err := f.find(id)
	if err != nil {
		errs <- err
		return results, errs
	}
	output, code := "", 0
	if f.toolOutput != nil {
		output, code = f.toolOutput(append(slices.Clone(c.config.Entrypoint), c.config.Cmd...))
	}
	c.logs, c.state = output, "exited"
	results <- container.WaitResponse{StatusCode: int64(code)}
	return results, errs
}

func (f *fakeEngine) CopyToContainer(_ context.Context, id, dst string, content io.Reader, _ container.CopyToContainerOptions) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	c, err := f.find(id)
	if err != nil {
		return err
	}
	reader := tar.NewReader(content)
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		body, _ := io.ReadAll(reader)
		c.files[strings.TrimSuffix(dst, "/")+"/"+header.Name] = body
	}
}

func (f *fakeEngine) ContainerExecCreate(_ context.Context, id string, options container.ExecOptions) (container.ExecCreateResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	c, err := f.find(id)
	if err != nil {
		return container.ExecCreateResponse{}, err
	}
	if c.state != "running" {
		return container.ExecCreateResponse{}, errdefs.ErrConflict.WithMessage("container is not running")
	}
	f.seq++
	execID := fmt.Sprintf("e%04d", f.seq)
	f.execs[execID] = c.id
	return container.ExecCreateResponse{ID: execID}, nil
}

func (f *fakeEngine) ContainerExecStart(context.Context, string, container.ExecStartOptions) error {
	return nil
}

func (f *fakeEngine) ContainerExecInspect(_ context.Context, execID string) (container.ExecInspect, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	c := f.containers[f.execs[execID]]
	if c == nil {
		return container.ExecInspect{}, errdefs.ErrNotFound
	}
	code := 0
	if _, injected := c.files[probePath]; !injected || (f.probeOK != nil && !f.probeOK(c)) {
		code = 1
	}
	return container.ExecInspect{ExecID: execID, ContainerID: c.id, ExitCode: code}, nil
}

func (f *fakeEngine) ImagePull(_ context.Context, ref string, options image.PullOptions) (io.ReadCloser, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pullAuth[ref] = options.RegistryAuth
	if message, failing := f.pullErr[ref]; failing {
		return io.NopCloser(strings.NewReader(`{"status":"Pulling"}` + "\n" + `{"errorDetail":{"message":"` + message + `"},"error":"` + message + `"}`)), nil
	}
	f.pulled = append(f.pulled, ref)
	return io.NopCloser(strings.NewReader(`{"status":"Downloaded newer image"}`)), nil
}

func (f *fakeEngine) ImageList(_ context.Context, options image.ListOptions) ([]image.Summary, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	ref := options.Filters.Get("reference")
	if len(ref) == 1 && slices.Contains(f.pulled, ref[0]) {
		return []image.Summary{{ID: ref[0]}}, nil
	}
	return nil, nil
}

func (f *fakeEngine) NetworkCreate(_ context.Context, name string, options network.CreateOptions) (network.CreateResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.networks[name]; ok {
		return network.CreateResponse{}, errdefs.ErrConflict.WithMessage("network with name " + name + " already exists")
	}
	f.networks[name] = &fakeNetwork{options: options}
	f.record("network create %s", name)
	return network.CreateResponse{ID: name}, nil
}

func (f *fakeEngine) NetworkInspect(_ context.Context, name string, _ network.InspectOptions) (network.Inspect, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	net, ok := f.networks[name]
	if !ok {
		return network.Inspect{}, errdefs.ErrNotFound.WithMessage("network " + name + " not found")
	}
	members := map[string]network.EndpointResource{}
	for _, id := range net.members {
		members[id] = network.EndpointResource{Name: f.containers[id].name}
	}
	return network.Inspect{Name: name, Internal: net.options.Internal, Options: net.options.Options, Labels: net.options.Labels,
		Containers: members}, nil
}

func (f *fakeEngine) NetworkRemove(_ context.Context, name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	net, ok := f.networks[name]
	if !ok {
		return errdefs.ErrNotFound
	}
	if len(net.members) > 0 {
		return errdefs.ErrPermissionDenied.WithMessage("network has active endpoints")
	}
	delete(f.networks, name)
	f.record("network remove %s", name)
	return nil
}

func (f *fakeEngine) NetworkConnect(_ context.Context, name, ref string, _ *network.EndpointSettings) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	net, ok := f.networks[name]
	if !ok {
		return errdefs.ErrNotFound
	}
	c, err := f.find(ref)
	if err != nil {
		return err
	}
	if slices.Contains(net.members, c.id) {
		return errdefs.ErrPermissionDenied.WithMessage("endpoint with name " + c.name + " already exists in network " + name)
	}
	net.members = append(net.members, c.id)
	c.networks = append(c.networks, name)
	f.record("connect %s %s", name, c.name)
	return nil
}

func (f *fakeEngine) NetworkDisconnect(_ context.Context, name, ref string, _ bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	net, ok := f.networks[name]
	if !ok {
		return errdefs.ErrNotFound
	}
	c, err := f.find(ref)
	if err != nil {
		return err
	}
	net.members = slices.DeleteFunc(net.members, func(m string) bool { return m == c.id })
	c.networks = slices.DeleteFunc(c.networks, func(n string) bool { return n == name })
	f.record("disconnect %s %s", name, c.name)
	return nil
}

func (f *fakeEngine) VolumeCreate(_ context.Context, options volume.CreateOptions) (volume.Volume, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.volumes[options.Name] = options.Labels
	f.record("volume create %s", options.Name)
	return volume.Volume{Name: options.Name, Labels: options.Labels}, nil
}

func (f *fakeEngine) VolumeInspect(_ context.Context, name string) (volume.Volume, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	labels, ok := f.volumes[name]
	if !ok {
		return volume.Volume{}, errdefs.ErrNotFound.WithMessage("no such volume")
	}
	return volume.Volume{Name: name, Labels: labels}, nil
}

func (f *fakeEngine) VolumeRemove(_ context.Context, name string, _ bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.volumes[name]; !ok {
		return errdefs.ErrNotFound
	}
	delete(f.volumes, name)
	f.record("volume remove %s", name)
	return nil
}

func (f *fakeEngine) VolumeList(_ context.Context, options volume.ListOptions) (volume.ListResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []*volume.Volume
	for name, labels := range f.volumes {
		if matchesLabels(labels, options.Filters.Get("label")) {
			out = append(out, &volume.Volume{Name: name, Labels: labels})
		}
	}
	return volume.ListResponse{Volumes: out}, nil
}

// byName finds a container the runtime created.
func (f *fakeEngine) byName(name string) *fakeContainer {
	f.mu.Lock()
	defer f.mu.Unlock()
	c, _ := f.find(name)
	return c
}

func (f *fakeEngine) appContainers(appID string) []*fakeContainer {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []*fakeContainer
	for _, c := range f.containers {
		if c.config.Labels[labelApp] == appID && c.config.Labels[labelComponent] == componentApp {
			out = append(out, c)
		}
	}
	slices.SortFunc(out, func(a, b *fakeContainer) int { return a.created.Compare(b.created) })
	return out
}
