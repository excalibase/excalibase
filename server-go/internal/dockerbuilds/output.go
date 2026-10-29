package dockerbuilds

import (
	"archive/tar"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/containerd/errdefs"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/pkg/stdcopy"

	"github.com/excalibase/provisioning-poc/internal/k8s"
	"github.com/excalibase/provisioning-poc/internal/sitebuilds"
)

const (
	// logTailLines and logStepBytes bound what one step keeps in the build history, as on Kubernetes.
	logTailLines = "4000"
	logStepBytes = 128 << 10
)

// logSteps are the steps whose output the history keeps, in order; the sidecar's refusals come first.
var logSteps = []struct{ step, title string }{
	{stepEgress, "network"}, {stepClone, "clone"}, {stepBuild, "build"}, {stepCollect, "collect"},
}

// Collect streams the packed output out of the collect step into w, failing past limit bytes.
func (r *Runner) Collect(ctx context.Context, buildID string, w io.Writer, limit int64) error {
	stream, _, err := r.engine.CopyFromContainer(ctx, containerName(buildID, stepCollect), k8s.SiteBuildArchivePath)
	if err != nil {
		return fmt.Errorf("read the packed output: %w", err)
	}
	defer stream.Close()
	archive := tar.NewReader(stream)
	header, err := archive.Next()
	if err != nil {
		return fmt.Errorf("read the packed output: %w", err)
	}
	if header.Typeflag != tar.TypeReg {
		return errors.New("the packed output is not a file")
	}
	if header.Size > limit {
		return sitebuilds.ErrOutputTooLarge
	}
	capped := &cappedWriter{out: w, limit: limit}
	if _, err := io.Copy(capped, archive); err != nil {
		if capped.overflowed {
			return sitebuilds.ErrOutputTooLarge
		}
		return fmt.Errorf("read the packed output: %w", err)
	}
	return nil
}

// cappedWriter refuses a write past its limit instead of truncating silently.
type cappedWriter struct {
	out        io.Writer
	limit      int64
	written    int64
	overflowed bool
}

func (c *cappedWriter) Write(chunk []byte) (int, error) {
	if c.written+int64(len(chunk)) > c.limit {
		c.overflowed = true
		return 0, sitebuilds.ErrOutputTooLarge
	}
	written, err := c.out.Write(chunk)
	c.written += int64(written)
	return written, err
}

// Logs answers each started step's tail, under a header per step.
func (r *Runner) Logs(ctx context.Context, buildID string) (string, error) {
	var out strings.Builder
	for _, entry := range logSteps {
		name := containerName(buildID, entry.step)
		inspect, err := r.engine.ContainerInspect(ctx, name)
		if errdefs.IsNotFound(err) {
			continue
		}
		if err != nil {
			return "", fmt.Errorf("inspect the %s step: %w", entry.title, err)
		}
		if inspect.ContainerJSONBase == nil || inspect.State == nil || inspect.State.Status == "created" {
			continue
		}
		tail, err := r.stepTail(ctx, name)
		if err != nil {
			return "", fmt.Errorf("read the %s step's log: %w", entry.title, err)
		}
		fmt.Fprintf(&out, "== %s ==\n%s\n", entry.title, tail)
	}
	return out.String(), nil
}

// stepTail reads a step's log through a bounded tail, so a build printing huge lines cannot fill provisioning's memory.
func (r *Runner) stepTail(ctx context.Context, name string) (string, error) {
	stream, err := r.engine.ContainerLogs(ctx, name, container.LogsOptions{ShowStdout: true, ShowStderr: true, Tail: logTailLines})
	if err != nil {
		return "", err
	}
	defer stream.Close()
	tail := &tailWriter{limit: logStepBytes}
	if _, err := stdcopy.StdCopy(tail, tail, stream); err != nil {
		return "", err
	}
	return tail.String(), nil
}

// tailWriter keeps at most twice limit bytes while writing, and the last limit when read.
type tailWriter struct {
	limit int
	data  []byte
	cut   bool
}

func (w *tailWriter) Write(chunk []byte) (int, error) {
	w.data = append(w.data, chunk...)
	if len(w.data) > 2*w.limit {
		w.data = append([]byte{}, w.data[len(w.data)-w.limit:]...)
		w.cut = true
	}
	return len(chunk), nil
}

func (w *tailWriter) String() string {
	text, cut := string(w.data), w.cut
	if len(text) > w.limit {
		text, cut = text[len(text)-w.limit:], true
	}
	if cut {
		return "…" + text
	}
	return text
}
