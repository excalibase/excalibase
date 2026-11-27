package dockerapps

import (
	"context"
	"fmt"
	"path"
	"slices"
	"strconv"

	"github.com/docker/docker/api/types/container"
)

const (
	tmpPath = "/tmp"
	// DefaultLogMaxSize and DefaultLogMaxFiles cap one app container's log at size x files:
	// 5m x 3 is 15 MiB, so a hundred apps hold at most 1.5 GiB of log between them.
	DefaultLogMaxSize  = "5m"
	DefaultLogMaxFiles = 3
	// scratchOptions: in memory, so no disk and no storage engine is involved and the kernel
	// refuses writes past the size. exec, because runtimes unpack and run native helpers from
	// there (sqlite-jdbc, Netty); nosuid and nodev stay.
	scratchOptions = "rw,exec,nosuid,nodev"
)

// scratchMounts are the only writable places an app has besides its disk: /tmp and any VOLUME
// the image declares, which the engine would otherwise make an anonymous, uncapped host volume.
// All of them are tmpfs, charged to the container's memory limit, so together they get one
// share of it (the operator's percentage), split evenly. A path the app's own disk covers keeps the disk.
func (r *Runtime) scratchMounts(spec containerSpec, imageVolumes []string) (map[string]string, error) {
	paths := []string{}
	for _, declared := range imageVolumes {
		cleaned := path.Clean("/" + declared)
		if cleaned == tmpPath || cleaned == "/"+probeDir || cleaned == spec.DiskMountPath || slices.Contains(paths, cleaned) {
			continue
		}
		paths = append(paths, cleaned)
	}
	slices.Sort(paths)
	budget := spec.MemoryBytes * int64(r.opts.TmpPercent) / 100
	each := budget / int64(1+len(paths))
	// size=0 means unlimited to tmpfs, so no memory limit, or a share that rounds to nothing, is refused.
	if each < 1 {
		return nil, fmt.Errorf("%w: %d%% of the %d byte memory limit leaves no room for /tmp and %d image volumes",
			ErrUnsupportedWorkload, r.opts.TmpPercent, spec.MemoryBytes, len(paths))
	}
	size := ",size=" + strconv.FormatInt(each, 10)
	mounts := map[string]string{tmpPath: scratchOptions + ",mode=1777" + size}
	for _, volumePath := range paths {
		mounts[volumePath] = scratchOptions + size
		// Podman copies the image's files up into the tmpfs; Docker rejects the option, and its tmpfs starts empty.
		if r.opts.TmpCopyUp {
			mounts[volumePath] += ",tmpcopyup"
		}
	}
	return mounts, nil
}

// imageVolumes are the VOLUME paths the image declares. An image the engine cannot describe
// is refused: its volumes would be unknown, and so uncapped.
func (r *Runtime) imageVolumes(ctx context.Context, ref string) ([]string, error) {
	inspect, err := r.engine.ImageInspect(ctx, ref)
	if err != nil {
		return nil, fmt.Errorf("read the volumes %s declares: %w", ref, err)
	}
	if inspect.Config == nil {
		return nil, nil
	}
	paths := make([]string, 0, len(inspect.Config.Volumes))
	for declared := range inspect.Config.Volumes {
		paths = append(paths, declared)
	}
	slices.Sort(paths)
	return paths, nil
}

// logConfig rotates each app container's log, so what an app prints cannot fill the host's disk.
func (r *Runtime) logConfig() container.LogConfig {
	return container.LogConfig{Type: "json-file", Config: map[string]string{
		"max-size": r.opts.LogMaxSize, "max-file": strconv.Itoa(r.opts.LogMaxFiles),
	}}
}
