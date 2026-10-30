package dockerbuilds

import (
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
	"k8s.io/apimachinery/pkg/api/resource"
)

// box is one build's sandbox on this host, whatever the build makes: an internal network whose only
// way out is the egress sidecar, a work volume, the disk watcher beside the steps, and the labels and
// time limit every container carries. Site builds run in one; app builds (EXC-677) are meant to as well,
// with their own steps on top.
type box struct {
	id       string
	labels   map[string]string
	diskKiB  int64
	lifetime int
}

// newBox labels a build's sandbox: managed, its component and id, the deadline and disk Status reads, and extra.
func (r *Runner) newBox(id, component string, extra map[string]string, timeout time.Duration, disk resource.Quantity) box {
	labels := map[string]string{
		labelManaged: "true", labelComponent: component, labelBuild: id,
		labelDeadline: strconv.FormatInt(r.opts.Now().Add(timeout).Unix(), 10), labelDisk: disk.String(),
	}
	for name, value := range extra {
		labels[name] = value
	}
	return box{id: id, labels: labels, diskKiB: disk.Value() / 1024, lifetime: int(timeout.Seconds())}
}

// deadlined runs cmd under the box's time limit, which the engine enforces even with provisioning gone.
func (b box) deadlined(cmd ...string) []string {
	return append([]string{"timeout", "-s", "KILL", strconv.Itoa(b.lifetime)}, cmd...)
}

// openBox lays the sandbox out and starts the watcher and sidecar; it answers the proxy URL steps go out through.
func (r *Runner) openBox(ctx context.Context, b box) (string, error) {
	if err := r.layOut(ctx, b); err != nil {
		return "", err
	}
	if err := r.startWatch(ctx, b); err != nil {
		return "", err
	}
	return r.startEgress(ctx, b)
}

// boxStep is a step container in the box: sandbox runtime, locked down, the work volume at /work,
// a /tmp, and the build network when online (else no network).
func (r *Runner) boxStep(b box, image string, cpuMillis, memory, pids int64, online bool) stepContainer {
	host := lockedHost(cpuMillis, memory, pids)
	host.Runtime = r.opts.SandboxRuntime
	host.Tmpfs = map[string]string{"/tmp": tmpfsOptions}
	host.Mounts = []mount.Mount{{Type: mount.TypeVolume, Source: r.workVolume(b.id), Target: workDir}}
	host.NetworkMode = "none"
	if online {
		host.NetworkMode = container.NetworkMode(r.buildNetwork(b.id))
	}
	config := &container.Config{Image: image, User: buildUser, WorkingDir: workDir, Labels: b.labels}
	return stepContainer{config: config, host: host}
}

func (r *Runner) layOut(ctx context.Context, b box) error {
	if err := r.ensureEgressNetwork(ctx); err != nil {
		return err
	}
	if _, err := r.engine.NetworkCreate(ctx, r.buildNetwork(b.id), r.buildNetworkOptions(b.labels)); err != nil {
		return fmt.Errorf("create the build network: %w", err)
	}
	if _, err := r.engine.VolumeCreate(ctx, volume.CreateOptions{Name: r.workVolume(b.id), Driver: "local", Labels: b.labels}); err != nil {
		return fmt.Errorf("create the work volume: %w", err)
	}
	return nil
}

// ensureEgressNetwork makes the one network with a route out, which only sidecars join.
func (r *Runner) ensureEgressNetwork(ctx context.Context) error {
	name := r.egressNetwork()
	if _, err := r.engine.NetworkInspect(ctx, name, network.InspectOptions{}); err == nil {
		return nil
	} else if !errdefs.IsNotFound(err) {
		return fmt.Errorf("inspect the egress network: %w", err)
	}
	options := network.CreateOptions{Driver: "bridge", Labels: map[string]string{labelManaged: "true", labelComponent: componentBuild}}
	if r.opts.Isolate {
		options.Options = map[string]string{"isolate": "true"}
	}
	if _, err := r.engine.NetworkCreate(ctx, name, options); err != nil && !errdefs.IsConflict(err) {
		return fmt.Errorf("create the egress network: %w", err)
	}
	return nil
}

// startEgress runs the sidecar and answers the proxy URL the steps use, by address:
// under gVisor a step may not reach the engine's resolver.
func (r *Runner) startEgress(ctx context.Context, b box) (string, error) {
	config := &container.Config{
		Image: r.opts.EgressImage, User: buildUser, Entrypoint: []string{"/bin/excalibase-build-egress"},
		Env: []string{
			"EGRESS_LISTEN=:" + egressPort,
			"EGRESS_EXTRA_DENY_CIDRS=" + strings.Join(r.opts.ExtraDenyCIDRs, ","),
			"EGRESS_ALLOW=" + strings.Join(r.opts.EgressAllow, ","),
			// Past the steps' own limit, so the time limit, not a vanished proxy, names a build that ran long.
			"EGRESS_LIFETIME=" + strconv.Itoa(b.lifetime+egressGraceSeconds) + "s",
		},
		Labels: b.labels,
	}
	host := lockedHost(toolSize[stepEgress].cpuMillis, toolSize[stepEgress].memory, toolPids)
	host.NetworkMode = container.NetworkMode(r.buildNetwork(b.id))
	if err := r.create(ctx, b.id, stepEgress, stepContainer{config: config, host: host}); err != nil {
		return "", err
	}
	name := containerName(b.id, stepEgress)
	if err := r.engine.NetworkConnect(ctx, r.egressNetwork(), name, nil); err != nil {
		return "", fmt.Errorf("connect the egress sidecar: %w", err)
	}
	if err := r.start(ctx, b.id, stepEgress); err != nil {
		return "", err
	}
	inspect, err := r.engine.ContainerInspect(ctx, name)
	if err != nil {
		return "", fmt.Errorf("inspect the egress sidecar: %w", err)
	}
	if inspect.NetworkSettings == nil {
		return "", errors.New("the engine answered no networks for the egress sidecar")
	}
	endpoint := inspect.NetworkSettings.Networks[r.buildNetwork(b.id)]
	if endpoint == nil || endpoint.IPAddress == "" {
		return "", errors.New("the egress sidecar has no address on the build network")
	}
	// Plain http to the forward proxy on the build's own network; HTTPS crosses it as CONNECT tunnels.
	return "http://" + endpoint.IPAddress + ":" + egressPort, nil //NOSONAR
}

// startWatch runs the disk watcher as the steps' own user, so it reads every folder they make (their 0700
// HOME too); one they make unreadable fails the build. No network, the volume read-only, its own PID namespace.
// It runs no build code, so it stays on the default runtime, where df sees the host's filesystem.
func (r *Runner) startWatch(ctx context.Context, b box) error {
	script := fmt.Sprintf(watchScript, b.lifetime, r.opts.HostDiskFloor/1024, b.diskKiB, int(r.opts.DiskCheckEvery.Seconds()))
	config := &container.Config{Image: r.opts.PrepareImage, User: buildUser, Cmd: []string{"sh", "-c", script}, Labels: b.labels}
	host := lockedHost(toolSize[stepWatch].cpuMillis, toolSize[stepWatch].memory, toolPids)
	host.NetworkMode = "none"
	host.Mounts = []mount.Mount{{Type: mount.TypeVolume, Source: r.workVolume(b.id), Target: workDir, ReadOnly: true}}
	if err := r.create(ctx, b.id, stepWatch, stepContainer{config: config, host: host}); err != nil {
		return err
	}
	return r.start(ctx, b.id, stepWatch)
}

// stopBox stops the named steps, the sidecar and the watcher; removeBox deletes them after.
func (r *Runner) stopBox(ctx context.Context, id string, steps []string) {
	timeout := stopSeconds
	for _, step := range append([]string{stepEgress, stepWatch}, steps...) {
		err := r.engine.ContainerStop(ctx, containerName(id, step), container.StopOptions{Timeout: &timeout})
		if err != nil && !errdefs.IsNotFound(err) {
			logf("site build %s: stop the %s step: %v", id, step, err)
		}
	}
}

// removeBox deletes the box's containers, work volume and network; one already gone is not an error.
func (r *Runner) removeBox(ctx context.Context, id string, steps []string) error {
	var errs []error
	for _, step := range append([]string{stepEgress, stepWatch}, steps...) {
		err := r.engine.ContainerRemove(ctx, containerName(id, step), container.RemoveOptions{Force: true})
		if err != nil && !errdefs.IsNotFound(err) {
			errs = append(errs, fmt.Errorf("remove the %s step: %w", step, err))
		}
	}
	if err := r.engine.VolumeRemove(ctx, r.workVolume(id), true); err != nil && !errdefs.IsNotFound(err) {
		errs = append(errs, fmt.Errorf("remove the work volume: %w", err))
	}
	if err := r.engine.NetworkRemove(ctx, r.buildNetwork(id)); err != nil && !errdefs.IsNotFound(err) {
		errs = append(errs, fmt.Errorf("remove the build network: %w", err))
	}
	return errors.Join(errs...)
}
