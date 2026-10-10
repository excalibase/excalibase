package dockerapps

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/containerd/errdefs"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/api/types/volume"
	"github.com/docker/docker/pkg/stdcopy"
	"k8s.io/apimachinery/pkg/api/resource"

	"github.com/excalibase/provisioning-poc/internal/apphost"
	"github.com/excalibase/provisioning-poc/internal/k8s"
)

// maxToolOutput bounds what a disk tool may print; du prints one line.
const maxToolOutput = 64 << 10

// CreateAppDisk makes the app's named volume once and opens it to whatever
// user the image runs as, as the Kubernetes init Job does. An app without a
// disk has nothing to create.
func (r *Runtime) CreateAppDisk(ctx context.Context, namespace string, app *apphost.App, _ string, opts k8s.DiskJobOptions) error {
	if app.Disk == nil {
		return nil
	}
	if err := r.requireProject(namespace, app.ProjectID); err != nil {
		return err
	}
	name := r.diskVolume(app.ID, app.Disk.Generation)
	if _, err := r.engine.VolumeInspect(ctx, name); err == nil {
		return nil
	} else if !errdefs.IsNotFound(err) {
		return fmt.Errorf("read disk %s: %w", name, err)
	}
	if err := r.createVolume(ctx, app, name, app.Disk.Generation); err != nil {
		return err
	}
	_, err := r.runTool(ctx, opts, []string{"chmod", "0777", "/disk"}, mount.Mount{Type: mount.TypeVolume, Source: name, Target: "/disk"})
	if err != nil {
		return errors.Join(err, r.engine.VolumeRemove(ctx, name, false))
	}
	return nil
}

func (r *Runtime) createVolume(ctx context.Context, app *apphost.App, name string, generation int) error {
	_, err := r.engine.VolumeCreate(ctx, volume.CreateOptions{
		Name: name, Driver: "local",
		Labels: map[string]string{labelManaged: "true", labelProject: app.ProjectID, labelApp: app.ID,
			labelGeneration: strconv.Itoa(generation)},
	})
	if err != nil {
		return fmt.Errorf("create disk %s: %w", name, err)
	}
	return nil
}

// GrowAppDisk: a volume on the host's filesystem has no size of its own, so
// the recorded size is the whole of it (ADR 0039); the volume must exist.
func (r *Runtime) GrowAppDisk(ctx context.Context, _ string, appID string, generation int, _ string) error {
	return r.requireVolume(ctx, r.diskVolume(appID, generation))
}

func (r *Runtime) requireVolume(ctx context.Context, name string) error {
	_, err := r.engine.VolumeInspect(ctx, name)
	if errdefs.IsNotFound(err) {
		return k8s.ErrAppDiskNotCreated
	}
	if err != nil {
		return fmt.Errorf("read disk %s: %w", name, err)
	}
	return nil
}

// AppDiskUsage reads what the disk holds; its size is the recorded one.
func (r *Runtime) AppDiskUsage(ctx context.Context, _ string, appID string, disk apphost.AppDisk, opts k8s.DiskJobOptions) (k8s.AppDiskUsage, error) {
	size, err := resource.ParseQuantity(disk.Size)
	if err != nil {
		return k8s.AppDiskUsage{}, fmt.Errorf("the disk's size %q: %w", disk.Size, err)
	}
	name := r.diskVolume(appID, disk.Generation)
	if err := r.requireVolume(ctx, name); err != nil {
		return k8s.AppDiskUsage{}, err
	}
	output, err := r.runTool(ctx, opts, []string{"du", "-sk", "/disk"},
		mount.Mount{Type: mount.TypeVolume, Source: name, Target: "/disk", ReadOnly: true})
	if err != nil {
		return k8s.AppDiskUsage{}, err
	}
	fields := strings.Fields(output)
	if len(fields) == 0 {
		return k8s.AppDiskUsage{}, fmt.Errorf("%w: du printed nothing", k8s.ErrAppDiskJob)
	}
	kibibytes, err := strconv.ParseInt(fields[0], 10, 64)
	if err != nil {
		return k8s.AppDiskUsage{}, fmt.Errorf("%w: du printed %q", k8s.ErrAppDiskJob, fields[0])
	}
	return k8s.AppDiskUsage{UsedBytes: kibibytes * 1024, SizeBytes: size.Value()}, nil
}

// CopyAppDisk copies the app's current disk onto a new generation's volume; the app is paused.
func (r *Runtime) CopyAppDisk(ctx context.Context, namespace string, app *apphost.App, to apphost.AppDisk, _ string, opts k8s.DiskJobOptions) error {
	if app.Disk == nil {
		return errors.New("the app has no disk")
	}
	if err := r.requireProject(namespace, app.ProjectID); err != nil {
		return err
	}
	from, target := r.diskVolume(app.ID, app.Disk.Generation), r.diskVolume(app.ID, to.Generation)
	if err := r.requireVolume(ctx, from); err != nil {
		return err
	}
	if err := r.requireVolume(ctx, target); errors.Is(err, k8s.ErrAppDiskNotCreated) {
		if err := r.createVolume(ctx, app, target, to.Generation); err != nil {
			return err
		}
	} else if err != nil {
		return err
	}
	_, err := r.runTool(ctx, opts, []string{"sh", "-c", "cp -a /from/. /to/ && chmod 0777 /to"},
		mount.Mount{Type: mount.TypeVolume, Source: from, Target: "/from", ReadOnly: true},
		mount.Mount{Type: mount.TypeVolume, Source: target, Target: "/to"})
	return err
}

// DeleteOtherAppDisks removes every generation of the app's disk but keep.
func (r *Runtime) DeleteOtherAppDisks(ctx context.Context, _ string, appID string, keep int, _ time.Duration) error {
	volumes, err := r.engine.VolumeList(ctx, volume.ListOptions{Filters: labelFilter(labelApp, appID)})
	if err != nil {
		return fmt.Errorf("list app disks: %w", err)
	}
	var errs []error
	for _, disk := range volumes.Volumes {
		if disk.Name == r.diskVolume(appID, keep) || !strings.HasPrefix(disk.Name, r.opts.VolumePrefix) {
			continue
		}
		if err := r.engine.VolumeRemove(ctx, disk.Name, false); err != nil && !errdefs.IsNotFound(err) {
			errs = append(errs, fmt.Errorf("remove disk %s: %w", disk.Name, err))
		}
	}
	return errors.Join(errs...)
}

// RepointAppDisk mounts claim in the paused app's containers in place of the disk they had.
func (r *Runtime) RepointAppDisk(ctx context.Context, _ string, appID, claim string) error {
	list, err := r.listApps(ctx, labelApp, appID)
	if err != nil {
		return err
	}
	if len(list) == 0 {
		return k8s.ErrAppNotDeployed
	}
	for _, summary := range list {
		if isUp(summary) {
			return fmt.Errorf("%w: pause the app before moving its disk", k8s.ErrAppNotPaused)
		}
	}
	for _, summary := range list {
		if summary.Labels[labelDisk] == claim {
			continue
		}
		if _, err := r.recreate(ctx, summary.ID, func(spec *containerSpec) { spec.DiskClaim = claim }); err != nil {
			return err
		}
	}
	return nil
}

// runTool runs one command in a throwaway container of the tools image: no
// network, the sandbox, nothing mounted but the disks named. It answers stdout.
func (r *Runtime) runTool(ctx context.Context, opts k8s.DiskJobOptions, cmd []string, mounts ...mount.Mount) (string, error) {
	name := "excalibase-app-disk-tool-" + shortHash(fmt.Sprint(time.Now().UnixNano(), cmd), 12)
	return r.runToolOn(ctx, opts, toolContainer{name: name, network: "none"}, cmd, mounts...)
}

// toolContainer names a tool container and the network it runs on ("none", or one it joins).
type toolContainer struct{ name, network string }

func (r *Runtime) runToolOn(ctx context.Context, opts k8s.DiskJobOptions, tool toolContainer, cmd []string, mounts ...mount.Mount) (string, error) {
	if opts.Timeout <= 0 {
		return "", fmt.Errorf("%w: a disk job needs a timeout", k8s.ErrAppDiskJob)
	}
	ctx, cancel := context.WithTimeout(ctx, opts.Timeout)
	defer cancel()
	id, err := r.createTool(ctx, tool, cmd, mounts)
	if err != nil {
		return "", err
	}
	defer func() { _ = r.remove(context.WithoutCancel(ctx), id) }()
	results, failures := r.engine.ContainerWait(ctx, id, container.WaitConditionNextExit)
	if err := r.engine.ContainerStart(ctx, id, container.StartOptions{}); err != nil {
		return "", fmt.Errorf("%w: start the disk tool: %v", k8s.ErrAppDiskJob, err)
	}
	var status container.WaitResponse
	select {
	case status = <-results:
	case err := <-failures:
		return "", fmt.Errorf("%w: wait for the disk tool: %v", k8s.ErrAppDiskJob, err)
	}
	stdout, stderr, err := r.toolOutput(ctx, id)
	if err != nil {
		return "", err
	}
	if status.StatusCode != 0 {
		return "", fmt.Errorf("%w: %s exited %d: %s", k8s.ErrAppDiskJob, cmd[0], status.StatusCode, strings.TrimSpace(stderr+stdout))
	}
	return stdout, nil
}

func (r *Runtime) createTool(ctx context.Context, tool toolContainer, cmd []string, mounts []mount.Mount) (string, error) {
	pids := int64(64)
	// The command replaces the image's entrypoint: the tools image may be provisioning's own.
	config := &container.Config{Image: r.opts.ToolsImage, Entrypoint: cmd[:1], Cmd: cmd[1:], User: "0",
		Labels: map[string]string{labelManaged: "true", labelComponent: componentDiskTool}}
	host := &container.HostConfig{
		NetworkMode: container.NetworkMode(tool.network), Mounts: mounts, Runtime: r.opts.SandboxRuntime,
		// cp -a keeps the default capabilities: it must read and chown files the app's user owns.
		SecurityOpt: []string{"no-new-privileges"}, CapDrop: []string{"NET_RAW"},
		Resources: container.Resources{Memory: 64 << 20, MemorySwap: 64 << 20, NanoCPUs: 500_000_000, PidsLimit: &pids},
	}
	created, err := r.engine.ContainerCreate(ctx, config, host, &network.NetworkingConfig{}, nil, tool.name)
	if err != nil {
		return "", fmt.Errorf("%w: create the disk tool: %v", k8s.ErrAppDiskJob, err)
	}
	return created.ID, nil
}

func (r *Runtime) toolOutput(ctx context.Context, id string) (string, string, error) {
	logs, err := r.engine.ContainerLogs(ctx, id, container.LogsOptions{ShowStdout: true, ShowStderr: true})
	if err != nil {
		return "", "", fmt.Errorf("%w: read the disk tool's output: %v", k8s.ErrAppDiskJob, err)
	}
	defer logs.Close()
	var stdout, stderr bytes.Buffer
	if _, err := stdcopy.StdCopy(&limitedWriter{&stdout, maxToolOutput}, &limitedWriter{&stderr, maxToolOutput}, logs); err != nil {
		return "", "", fmt.Errorf("%w: read the disk tool's output: %v", k8s.ErrAppDiskJob, err)
	}
	return stdout.String(), stderr.String(), nil
}

// limitedWriter keeps the first limit bytes and drops the rest.
type limitedWriter struct {
	buffer *bytes.Buffer
	limit  int
}

func (w *limitedWriter) Write(chunk []byte) (int, error) {
	if room := w.limit - w.buffer.Len(); room > 0 {
		w.buffer.Write(chunk[:min(room, len(chunk))])
	}
	return len(chunk), nil
}
