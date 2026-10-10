package dockerapps

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/containerd/errdefs"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/api/types/network"
)

const (
	// probePath is where the readiness probe is copied in before a container starts.
	probeDir  = ".excalibase"
	probePath = "/" + probeDir + "/probe"
	// execWait bounds one probe; the probe itself times out after three seconds.
	execWait = 10 * time.Second
)

// listApps lists every app container (running or not) matching the label pairs.
func (r *Runtime) listApps(ctx context.Context, pairs ...string) ([]container.Summary, error) {
	pairs = append([]string{labelComponent, componentApp}, pairs...)
	list, err := r.engine.ContainerList(ctx, container.ListOptions{All: true, Filters: labelFilter(pairs...)})
	if err != nil {
		return nil, fmt.Errorf("list app containers: %w", err)
	}
	return list, nil
}

func nameOf(summary container.Summary) string {
	if len(summary.Names) == 0 {
		return summary.ID
	}
	return strings.TrimPrefix(summary.Names[0], "/")
}

func isUp(summary container.Summary) bool {
	return summary.State == container.StateRunning || summary.State == container.StateRestarting
}

// pullImage pulls on every deploy with the project's own credential: an image
// already on the host must never run for a project that cannot pull it.
func (r *Runtime) pullImage(ctx context.Context, ref string, auth *registryAuth) error {
	stream, err := r.engine.ImagePull(ctx, ref, image.PullOptions{RegistryAuth: auth.header()})
	if err != nil {
		return fmt.Errorf("pull %s: %w", ref, err)
	}
	defer stream.Close()
	decoder := json.NewDecoder(stream)
	for {
		var message struct {
			Error       string `json:"error"`
			ErrorDetail struct {
				Message string `json:"message"`
			} `json:"errorDetail"`
		}
		err := decoder.Decode(&message)
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("pull %s: %w", ref, err)
		}
		if message.Error != "" || message.ErrorDetail.Message != "" {
			return fmt.Errorf("pull %s: %s", ref, strings.TrimSpace(message.Error+" "+message.ErrorDetail.Message))
		}
	}
}

func (r *Runtime) appLabels(spec containerSpec, replica int) map[string]string {
	probe, _ := json.Marshal(spec.Probe)
	labels := map[string]string{
		labelManaged: "true", labelComponent: componentApp, labelProject: spec.Project, labelApp: spec.AppID,
		labelAppName: spec.AppName, labelDeploy: spec.DeployID, labelReplica: strconv.Itoa(replica), labelTier: spec.Tier,
		labelCPURequest: strconv.FormatInt(spec.CPURequestMilli, 10), labelMemoryRequest: strconv.FormatInt(spec.MemoryRequest, 10),
		labelPort: strconv.Itoa(spec.Port), labelProbe: string(probe),
	}
	if spec.DiskClaim != "" {
		labels[labelDisk], labels[labelDiskMount] = spec.DiskClaim, spec.DiskMountPath
	}
	return labels
}

// hostConfig is the app's isolation: its sandbox, its project's network, its
// tier's limits, and nothing of the host's.
func (r *Runtime) hostConfig(spec containerSpec) *container.HostConfig {
	pids := appPidsLimit
	host := &container.HostConfig{
		NetworkMode:   container.NetworkMode(r.networkName(spec.Project)),
		RestartPolicy: container.RestartPolicy{Name: container.RestartPolicyUnlessStopped},
		SecurityOpt:   []string{"no-new-privileges"},
		Runtime:       r.opts.SandboxRuntime,
		Resources: container.Resources{
			Memory: spec.MemoryBytes, MemorySwap: spec.MemoryBytes, NanoCPUs: spec.NanoCPUs, PidsLimit: &pids,
		},
	}
	// Inside the sandbox an app keeps what Kubernetes gives it; without one it keeps only low ports.
	if r.opts.SandboxRuntime != "" {
		host.CapDrop = []string{"NET_RAW"}
	} else {
		host.CapDrop, host.CapAdd = []string{"ALL"}, []string{"NET_BIND_SERVICE"}
	}
	if spec.DiskClaim != "" {
		host.Mounts = []mount.Mount{{Type: mount.TypeVolume, Source: r.volumeName(spec.DiskClaim), Target: spec.DiskMountPath}}
	}
	return host
}

// createAppContainer creates one replica with the probe copied in; it is not started.
func (r *Runtime) createAppContainer(ctx context.Context, spec containerSpec, replica int) (string, error) {
	config := &container.Config{Image: spec.Image, Env: spec.Env, Labels: r.appLabels(spec, replica)}
	if len(spec.Args) > 0 {
		config.Cmd = spec.Args
	}
	endpoints := &network.NetworkingConfig{EndpointsConfig: map[string]*network.EndpointSettings{r.networkName(spec.Project): {}}}
	name := appContainerName(spec.Project, spec.AppID, spec.DeployID, replica)
	created, err := r.engine.ContainerCreate(ctx, config, r.hostConfig(spec), endpoints, nil, name)
	if err != nil {
		return "", fmt.Errorf("create %s: %w", name, err)
	}
	if err := r.injectProbe(ctx, created.ID); err != nil {
		return "", errors.Join(err, r.remove(ctx, created.ID))
	}
	return created.ID, nil
}

func (r *Runtime) injectProbe(ctx context.Context, id string) error {
	var archive bytes.Buffer
	writer := tar.NewWriter(&archive)
	entries := []struct {
		header tar.Header
		body   []byte
	}{
		{tar.Header{Name: probeDir + "/", Mode: 0o755, Typeflag: tar.TypeDir}, nil},
		{tar.Header{Name: probeDir + "/probe", Mode: 0o755, Typeflag: tar.TypeReg, Size: int64(len(r.opts.ProbeBinary))}, r.opts.ProbeBinary},
	}
	for _, entry := range entries {
		if err := writer.WriteHeader(&entry.header); err != nil {
			return fmt.Errorf("pack the readiness probe: %w", err)
		}
		if _, err := writer.Write(entry.body); err != nil {
			return fmt.Errorf("pack the readiness probe: %w", err)
		}
	}
	if err := writer.Close(); err != nil {
		return fmt.Errorf("pack the readiness probe: %w", err)
	}
	if err := r.engine.CopyToContainer(ctx, id, "/", &archive, container.CopyToContainerOptions{}); err != nil {
		return fmt.Errorf("copy the readiness probe into %s: %w", id, err)
	}
	return nil
}

// probeReady runs the probe inside the container; any failure to run it is "not ready".
func (r *Runtime) probeReady(ctx context.Context, id string, probe probeSpec) bool {
	exec, err := r.engine.ContainerExecCreate(ctx, id, container.ExecOptions{Cmd: append([]string{probePath}, probe.args()...)})
	if err != nil {
		return false
	}
	// Detached: Podman refuses an attached start of an exec that attaches no stream.
	if err := r.engine.ContainerExecStart(ctx, exec.ID, container.ExecStartOptions{Detach: true}); err != nil {
		return false
	}
	deadline := time.Now().Add(execWait)
	for time.Now().Before(deadline) {
		inspect, err := r.engine.ContainerExecInspect(ctx, exec.ID)
		if err != nil {
			return false
		}
		if !inspect.Running {
			return inspect.ExitCode == 0
		}
		select {
		case <-ctx.Done():
			return false
		case <-time.After(100 * time.Millisecond):
		}
	}
	return false
}

func probeOf(labels map[string]string) (probeSpec, bool) {
	var probe probeSpec
	if err := json.Unmarshal([]byte(labels[labelProbe]), &probe); err != nil || probe.Port == 0 {
		return probeSpec{}, false
	}
	return probe, true
}

func (r *Runtime) start(ctx context.Context, ids []string) error {
	for _, id := range ids {
		if err := r.engine.ContainerStart(ctx, id, container.StartOptions{}); err != nil {
			return fmt.Errorf("start %s: %w", id, err)
		}
	}
	return nil
}

func (r *Runtime) stop(ctx context.Context, id string) error {
	timeout := appStopSeconds
	if err := r.engine.ContainerStop(ctx, id, container.StopOptions{Timeout: &timeout}); err != nil && !errdefs.IsNotFound(err) {
		return fmt.Errorf("stop %s: %w", id, err)
	}
	return nil
}

func (r *Runtime) remove(ctx context.Context, id string) error {
	if err := r.engine.ContainerRemove(ctx, id, container.RemoveOptions{Force: true}); err != nil && !errdefs.IsNotFound(err) {
		return fmt.Errorf("remove %s: %w", id, err)
	}
	return nil
}

// retire stops each container gracefully, then removes it.
func (r *Runtime) retire(ctx context.Context, containers []container.Summary) error {
	var errs []error
	for _, summary := range containers {
		if isUp(summary) {
			if err := r.stop(ctx, summary.ID); err != nil {
				errs = append(errs, err)
				continue
			}
		}
		if err := r.remove(ctx, summary.ID); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// where keeps the containers keep accepts.
func where(list []container.Summary, keep func(container.Summary) bool) []container.Summary {
	return slices.DeleteFunc(slices.Clone(list), func(summary container.Summary) bool { return !keep(summary) })
}
