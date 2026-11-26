package dockerapps

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"

	"github.com/containerd/errdefs"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/api/types/volume"

	"github.com/excalibase/provisioning-poc/internal/engineproxy"
)

const probeVolumeStem = "probe-"

// probeVolume is the platform's own volume holding the readiness probe and, when the edge signs
// with its own CA, that CA's root. Every app mounts it read-only at /.excalibase: the probe is not
// copied into a path the app can write, so it is no disk an app can fill. Its name carries a
// digest of what it holds, so a new probe or CA makes a new volume and a held name is never stale.
func (r *Runtime) probeVolume(ctx context.Context, image string, edgeCA []byte) (string, error) {
	files := r.probeFiles(edgeCA)
	name := r.volumeName(probeVolumeStem + files.digest())
	r.probeMu.Lock()
	defer r.probeMu.Unlock()
	if _, err := r.engine.VolumeInspect(ctx, name); err == nil {
		return name, nil
	} else if !errdefs.IsNotFound(err) {
		return "", fmt.Errorf("look for the probe volume %s: %w", name, err)
	}
	if _, err := r.engine.VolumeCreate(ctx, volume.CreateOptions{Name: name, Labels: map[string]string{
		labelManaged: "true", engineproxy.SharedReadOnlyLabel: "true",
	}}); err != nil {
		return "", fmt.Errorf("create the probe volume %s: %w", name, err)
	}
	if err := r.fillProbeVolume(ctx, name, image, files); err != nil {
		return "", errors.Join(err, r.engine.VolumeRemove(ctx, name, false))
	}
	r.removeStaleProbeVolumes(ctx, name)
	return name, nil
}

// fillProbeVolume copies the files in through a container that is created, never started: it
// runs nothing, and only it may mount the volume writable. RemoveVolumes never takes a named volume.
func (r *Runtime) fillProbeVolume(ctx context.Context, name, image string, files probeFiles) error {
	filler := name + "-fill"
	host := &container.HostConfig{
		NetworkMode: "none", Runtime: r.opts.SandboxRuntime, ReadonlyRootfs: true,
		Mounts: []mount.Mount{{Type: mount.TypeVolume, Source: name, Target: "/" + probeDir, VolumeOptions: &mount.VolumeOptions{NoCopy: true}}},
	}
	created, err := r.engine.ContainerCreate(ctx, &container.Config{Image: image, Labels: map[string]string{
		labelManaged: "true", engineproxy.SharedFillLabel: "true",
	}}, host, &network.NetworkingConfig{}, nil, filler)
	if err != nil {
		return fmt.Errorf("create the probe volume's filler: %w", err)
	}
	copyErr := r.copyProbeFiles(ctx, created.ID, files)
	removeErr := r.engine.ContainerRemove(ctx, created.ID, container.RemoveOptions{Force: true, RemoveVolumes: true})
	return errors.Join(copyErr, removeErr)
}

func (r *Runtime) copyProbeFiles(ctx context.Context, id string, files probeFiles) error {
	var archive bytes.Buffer
	writer := tar.NewWriter(&archive)
	for _, file := range files {
		header := tar.Header{Name: file.name, Mode: file.mode, Typeflag: tar.TypeReg, Size: int64(len(file.body))}
		if err := writer.WriteHeader(&header); err != nil {
			return fmt.Errorf("pack the readiness probe: %w", err)
		}
		if _, err := writer.Write(file.body); err != nil {
			return fmt.Errorf("pack the readiness probe: %w", err)
		}
	}
	if err := writer.Close(); err != nil {
		return fmt.Errorf("pack the readiness probe: %w", err)
	}
	if err := r.engine.CopyToContainer(ctx, id, "/"+probeDir, &archive, container.CopyToContainerOptions{}); err != nil {
		return fmt.Errorf("fill the probe volume: %w", err)
	}
	return nil
}

// removeStaleProbeVolumes drops probe volumes of an earlier probe or CA; one an app still mounts stays.
func (r *Runtime) removeStaleProbeVolumes(ctx context.Context, keep string) {
	list, err := r.engine.VolumeList(ctx, volume.ListOptions{Filters: labelFilter(engineproxy.SharedReadOnlyLabel, "true")})
	if err != nil {
		return
	}
	for _, stale := range list.Volumes {
		if stale.Name != keep {
			_ = r.engine.VolumeRemove(ctx, stale.Name, false)
		}
	}
}

type probeFile struct {
	name string
	mode int64
	body []byte
}

type probeFiles []probeFile

func (r *Runtime) probeFiles(edgeCA []byte) probeFiles {
	files := probeFiles{{name: "probe", mode: 0o755, body: r.opts.ProbeBinary}}
	if edgeCA != nil {
		files = append(files, probeFile{name: "edge-ca.crt", mode: 0o644, body: edgeCA})
	}
	return files
}

func (files probeFiles) digest() string {
	sum := sha256.New()
	for _, file := range files {
		fmt.Fprintf(sum, "%s\x00%d\x00", file.name, len(file.body))
		sum.Write(file.body)
	}
	return hex.EncodeToString(sum.Sum(nil))[:16]
}
