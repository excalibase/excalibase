package dockerapps

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/containerd/errdefs"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/api/types/volume"

	"github.com/excalibase/provisioning-poc/internal/engineproxy"
)

const (
	probeVolumeStem  = "probe-"
	probeReadySuffix = "-ready"
	probeFillSuffix  = "-fill"
)

// probeVolume is the platform's own volume holding the readiness probe and, when the edge signs
// with its own CA, that CA's root. Every app mounts it read-only at /.excalibase: the probe is not
// copied into a path the app can write, so it is no disk an app can fill. Its name carries a
// digest of what it holds, so a new probe or CA makes a new volume and a held name is never stale.
//
// A second, empty volume of the same name plus "-ready" is made only once the files are in. A
// probe volume without it is half filled (a crash between the two) and is removed and filled again.
func (r *Runtime) probeVolume(ctx context.Context, edgeCA []byte) (string, error) {
	files := r.probeFiles(edgeCA)
	name := r.volumeName(probeVolumeStem + files.digest())
	ready := name + probeReadySuffix
	r.probeMu.Lock()
	defer r.probeMu.Unlock()
	if _, err := r.engine.VolumeInspect(ctx, ready); err == nil {
		return name, nil
	} else if !errdefs.IsNotFound(err) {
		return "", fmt.Errorf("look for the probe volume %s: %w", ready, err)
	}
	// A filler left by a crash mid-copy keeps the volume "in use" without filling it: it goes first.
	if err := r.engine.ContainerRemove(ctx, name+probeFillSuffix, container.RemoveOptions{Force: true}); err != nil && !errdefs.IsNotFound(err) {
		return "", fmt.Errorf("remove the probe volume's leftover filler: %w", err)
	}
	// Whatever is there was never finished, unless an app mounts it: only apps and the filler ever
	// mount it, and an app is only created on a filled volume, so one still in use is filled and
	// only lost its marker.
	switch err := r.engine.VolumeRemove(ctx, name, false); {
	case err == nil || errdefs.IsNotFound(err):
	case errdefs.IsConflict(err):
		return name, r.makeSharedVolume(ctx, ready)
	default:
		return "", fmt.Errorf("remove the half-filled probe volume %s: %w", name, err)
	}
	if err := r.makeSharedVolume(ctx, name); err != nil {
		return "", err
	}
	if err := r.fillProbeVolume(ctx, name, files); err != nil {
		return "", errors.Join(err, r.engine.VolumeRemove(ctx, name, false))
	}
	if err := r.makeSharedVolume(ctx, ready); err != nil {
		return "", errors.Join(err, r.engine.VolumeRemove(ctx, name, false))
	}
	r.removeStaleProbeVolumes(ctx, name, ready)
	return name, nil
}

func (r *Runtime) makeSharedVolume(ctx context.Context, name string) error {
	if _, err := r.engine.VolumeCreate(ctx, volume.CreateOptions{Name: name, Labels: map[string]string{
		labelManaged: "true", engineproxy.SharedReadOnlyLabel: "true",
	}}); err != nil {
		return fmt.Errorf("create the probe volume %s: %w", name, err)
	}
	return nil
}

// fillProbeVolume copies the files in through a container of the platform's tools image, created and never
// started (the proxy refuses to start it): it runs nothing, and only it may mount the volume writable. RemoveVolumes never takes a named volume.
func (r *Runtime) fillProbeVolume(ctx context.Context, name string, files probeFiles) error {
	filler := name + probeFillSuffix
	host := &container.HostConfig{
		NetworkMode: "none", Runtime: r.opts.SandboxRuntime, ReadonlyRootfs: true,
		Mounts: []mount.Mount{{Type: mount.TypeVolume, Source: name, Target: "/" + probeDir, VolumeOptions: &mount.VolumeOptions{NoCopy: true}}},
	}
	created, err := r.engine.ContainerCreate(ctx, &container.Config{Image: r.opts.ToolsImage, Labels: map[string]string{
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

// removeStaleProbeVolumes drops probe volumes of an earlier probe or CA, each with its marker. One an
// app still mounts cannot be removed and keeps its marker: if that digest returns it is still known to
// be filled, and the in-use volume is not recreated. A marker whose volume is gone goes too.
func (r *Runtime) removeStaleProbeVolumes(ctx context.Context, keep ...string) {
	list, err := r.engine.VolumeList(ctx, volume.ListOptions{Filters: labelFilter(engineproxy.SharedReadOnlyLabel, "true")})
	if err != nil {
		return
	}
	names := map[string]bool{}
	for _, shared := range list.Volumes {
		names[shared.Name] = true
	}
	for name := range names {
		if slices.Contains(keep, name) || strings.HasSuffix(name, probeReadySuffix) {
			continue
		}
		if err := r.engine.VolumeRemove(ctx, name, false); err == nil || errdefs.IsNotFound(err) {
			delete(names, name)
		}
	}
	for name := range names {
		marker := strings.TrimSuffix(name, probeReadySuffix)
		if slices.Contains(keep, name) || marker == name || names[marker] {
			continue
		}
		_ = r.engine.VolumeRemove(ctx, name, false)
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
