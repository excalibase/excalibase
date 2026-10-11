package service

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/client"
	"github.com/docker/docker/pkg/stdcopy"
)

// ContainerStreamer runs a command in a container with its stdout streamed to
// dst, so a dump of any size never sits in the platform's memory.
type ContainerStreamer interface {
	StreamExec(ctx context.Context, container string, cmd []string, dst io.Writer) error
}

// DockerStreamer streams execs through the engine's API.
type DockerStreamer struct{ sdk dockerSDK }

// NewDockerStreamer wraps a live SDK client.
func NewDockerStreamer(c *client.Client) *DockerStreamer {
	return &DockerStreamer{sdk: &realDockerSDK{c: c}}
}

// streamExitWait bounds the wait for an exec's exit code once its output ended.
const streamExitWait = 30 * time.Second

// StreamExec copies the command's stdout to dst and fails unless it exits 0;
// the error carries the start of its stderr.
func (d *DockerStreamer) StreamExec(ctx context.Context, target string, cmd []string, dst io.Writer) error {
	exec, err := d.sdk.ContainerExecCreate(ctx, target, container.ExecOptions{Cmd: cmd, AttachStdout: true, AttachStderr: true})
	if err != nil {
		return fmt.Errorf("exec create: %w", err)
	}
	attached, err := d.sdk.ContainerExecAttach(ctx, exec.ID, container.ExecStartOptions{})
	if err != nil {
		return fmt.Errorf("exec attach: %w", err)
	}
	if attached.Closer != nil {
		defer attached.Closer.Close()
	}
	var stderr bytes.Buffer
	if _, err := stdcopy.StdCopy(dst, &limitedBuffer{buffer: &stderr, limit: 4096}, attached.Reader); err != nil {
		return fmt.Errorf("exec output: %w (stderr: %s)", err, stderr.String())
	}
	deadline := time.Now().Add(streamExitWait)
	for time.Now().Before(deadline) {
		inspect, err := d.sdk.ContainerExecInspect(ctx, exec.ID)
		if err != nil {
			return fmt.Errorf("exec inspect: %w", err)
		}
		if !inspect.Running {
			if inspect.ExitCode != 0 {
				return fmt.Errorf("exited %d: %s", inspect.ExitCode, stderr.String())
			}
			return nil
		}
		time.Sleep(200 * time.Millisecond)
	}
	return fmt.Errorf("exec did not report its exit within %s", streamExitWait)
}

// limitedBuffer keeps the first limit bytes and drops the rest.
type limitedBuffer struct {
	buffer *bytes.Buffer
	limit  int
}

func (l *limitedBuffer) Write(p []byte) (int, error) {
	if room := l.limit - l.buffer.Len(); room > 0 {
		l.buffer.Write(p[:min(room, len(p))])
	}
	return len(p), nil
}
