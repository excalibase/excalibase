package provisioner

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/containerd/errdefs"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/pkg/stdcopy"
)

// ContainerSpec is everything provisioning sets on a container it creates.
type ContainerSpec struct {
	Name  string
	Image string
	Env   map[string]string
	// Cmd replaces the image's command; nil keeps it.
	Cmd []string
	// Ports maps container port to host port ("" lets the engine choose),
	// published on the client's bind address.
	Ports  map[string]string
	Limits ContainerLimits
	// DataVolume mounts an anonymous volume there, removed with the container.
	DataVolume string
	// StopSignal replaces the image's; empty keeps it.
	StopSignal string
	// NetnsOf runs the container in that container's network namespace
	// instead of on a network; it then publishes no ports of its own.
	NetnsOf string
}

// ContainerState is what an inspect reports about a container.
type ContainerState struct {
	Found     bool
	Running   bool
	StartedAt time.Time
	// HostPorts maps a published container port ("5432") to its host port.
	HostPorts map[string]int
}

// ContainerState inspects the container. A container the engine does not
// know is reported as not found, not as an error.
func (r *RealDockerClient) ContainerState(ctx context.Context, id string) (ContainerState, error) {
	insp, err := r.c.ContainerInspect(ctx, id)
	if errdefs.IsNotFound(err) {
		return ContainerState{}, nil
	}
	if err != nil {
		return ContainerState{}, fmt.Errorf("inspect container %s: %w", id, err)
	}
	state := ContainerState{Found: true, HostPorts: map[string]int{}}
	if insp.State != nil {
		state.Running = insp.State.Running
		state.StartedAt, _ = time.Parse(time.RFC3339Nano, insp.State.StartedAt)
	}
	if insp.NetworkSettings != nil {
		for port, bindings := range insp.NetworkSettings.Ports {
			for _, binding := range bindings {
				if hostPort, err := strconv.Atoi(binding.HostPort); err == nil && hostPort > 0 {
					state.HostPorts[port.Port()] = hostPort
					break
				}
			}
		}
	}
	return state, nil
}

// execOutputLimit bounds what an exec's output may hold in memory.
const execOutputLimit = 64 << 10

// ExecInContainerStdin runs cmd with stdin fed to it and waits for it to end,
// returning its output. A non-zero exit is an error carrying that output. The
// input never appears in the exec's command line, which the engine logs.
func (r *RealDockerClient) ExecInContainerStdin(ctx context.Context, id string, cmd []string, stdin string) (string, error) {
	create, err := r.c.ContainerExecCreate(ctx, id, container.ExecOptions{
		Cmd: cmd, AttachStdin: true, AttachStdout: true, AttachStderr: true,
	})
	if err != nil {
		return "", fmt.Errorf("exec create: %w", err)
	}
	attached, err := r.c.ContainerExecAttach(ctx, create.ID, container.ExecStartOptions{})
	if err != nil {
		return "", fmt.Errorf("exec attach: %w", err)
	}
	defer attached.Close()
	if _, err := io.Copy(attached.Conn, strings.NewReader(stdin)); err != nil {
		return "", fmt.Errorf("exec stdin: %w", err)
	}
	if err := attached.CloseWrite(); err != nil {
		return "", fmt.Errorf("exec stdin close: %w", err)
	}
	var stdout, stderr bytes.Buffer
	if _, err := stdcopy.StdCopy(&limitedWriter{&stdout}, &limitedWriter{&stderr}, attached.Reader); err != nil {
		return "", fmt.Errorf("exec output: %w", err)
	}
	return r.execResult(ctx, create.ID, stdout.String(), stderr.String())
}

// execResult waits for the exec's exit code once its streams have ended.
func (r *RealDockerClient) execResult(ctx context.Context, execID, stdout, stderr string) (string, error) {
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		inspect, err := r.c.ContainerExecInspect(ctx, execID)
		if err != nil {
			return "", fmt.Errorf("exec inspect: %w", err)
		}
		if !inspect.Running {
			if inspect.ExitCode != 0 {
				return stdout, fmt.Errorf("exit code %d: %s", inspect.ExitCode, strings.TrimSpace(stderr+stdout))
			}
			return stdout, nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return "", fmt.Errorf("exec did not report its exit code within 10s")
}

// limitedWriter keeps the first execOutputLimit bytes and drops the rest, so
// a chatty command cannot grow the platform's memory.
type limitedWriter struct{ buffer *bytes.Buffer }

func (w *limitedWriter) Write(p []byte) (int, error) {
	if room := execOutputLimit - w.buffer.Len(); room > 0 {
		w.buffer.Write(p[:min(room, len(p))])
	}
	return len(p), nil
}
