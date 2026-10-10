package provisioner

import (
	"context"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"
)

// fakeEngine is a Docker engine in memory where a container's id is its name,
// as the real engine accepts either. It records every call in order.
type fakeEngine struct {
	mu         sync.Mutex
	containers map[string]*fakeContainer
	specs      map[string]ContainerSpec
	calls      []string
	stdin      []string
	copied     map[string][]byte
	// execCode answers an exec; nil answers 0.
	execCode func(container string, cmd []string) int
	// failOn makes the named call ("create:<name>", "start:<name>") fail.
	failOn string
	clock  time.Time
}

type fakeContainer struct {
	running   bool
	startedAt time.Time
}

func newFakeEngine() *fakeEngine {
	return &fakeEngine{
		containers: map[string]*fakeContainer{},
		specs:      map[string]ContainerSpec{},
		copied:     map[string][]byte{},
		clock:      time.Date(2026, 10, 10, 0, 0, 0, 0, time.UTC),
	}
}

func (f *fakeEngine) record(call string) error {
	f.calls = append(f.calls, call)
	if f.failOn == call {
		return fmt.Errorf("%s failed", call)
	}
	return nil
}

func (f *fakeEngine) CreateContainer(ctx context.Context, name, image string, env, ports map[string]string, limits ContainerLimits) (string, error) {
	return f.CreateContainerSpec(ctx, ContainerSpec{Name: name, Image: image, Env: env, Ports: ports, Limits: limits})
}

func (f *fakeEngine) CreateContainerSpec(_ context.Context, spec ContainerSpec) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.record("create:" + spec.Name); err != nil {
		return "", err
	}
	if _, taken := f.containers[spec.Name]; taken {
		return "", fmt.Errorf("container name %s is already in use", spec.Name)
	}
	f.containers[spec.Name] = &fakeContainer{}
	f.specs[spec.Name] = spec
	return spec.Name, nil
}

func (f *fakeEngine) StartContainer(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.record("start:" + id); err != nil {
		return err
	}
	c, ok := f.containers[id]
	if !ok {
		return fmt.Errorf("no such container %s", id)
	}
	if !c.running {
		f.clock = f.clock.Add(time.Second)
		c.running, c.startedAt = true, f.clock
	}
	return nil
}

func (f *fakeEngine) StopContainer(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.record("stop:" + id); err != nil {
		return err
	}
	if c, ok := f.containers[id]; ok {
		c.running = false
	}
	return nil
}

func (f *fakeEngine) RemoveContainer(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.record("remove:" + id); err != nil {
		return err
	}
	delete(f.containers, id)
	return nil
}

func (f *fakeEngine) ContainerStatus(_ context.Context, id string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	c, ok := f.containers[id]
	switch {
	case !ok:
		return containerNotFound, nil
	case c.running:
		return containerRunning, nil
	}
	return "stopped", nil
}

func (f *fakeEngine) ContainerState(_ context.Context, id string) (ContainerState, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	c, ok := f.containers[id]
	if !ok {
		return ContainerState{}, nil
	}
	return ContainerState{Found: true, Running: c.running, StartedAt: c.startedAt}, nil
}

func (f *fakeEngine) WaitForHealthy(context.Context, string) error { return nil }

func (f *fakeEngine) ExecInContainer(_ context.Context, id string, cmd []string) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, "exec:"+id+":"+cmd[0])
	if f.execCode != nil {
		return f.execCode(id, cmd), nil
	}
	return 0, nil
}

func (f *fakeEngine) ExecInContainerStdin(_ context.Context, id string, cmd []string, stdin string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.record("stdin:" + id); err != nil {
		return "", err
	}
	f.stdin = append(f.stdin, stdin)
	return "", nil
}

func (f *fakeEngine) CopyToContainer(_ context.Context, id, dst string, content io.Reader) error {
	body, err := io.ReadAll(content)
	if err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.record("copy:" + id + ":" + dst); err != nil {
		return err
	}
	f.copied[id+":"+dst] = append(f.copied[id+":"+dst], body...)
	return nil
}

func (f *fakeEngine) CopyFromContainer(_ context.Context, id, src string) (io.ReadCloser, error) {
	return io.NopCloser(strings.NewReader("")), fmt.Errorf("copy from %s:%s not faked", id, src)
}

// indexOf is the position of the first call with this prefix, or -1.
func (f *fakeEngine) indexOf(prefix string) int {
	for i, call := range f.calls {
		if strings.HasPrefix(call, prefix) {
			return i
		}
	}
	return -1
}
