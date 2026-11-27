package engineproxy

import (
	"context"
	"errors"
	"net/url"
	"strings"
)

// A named volume belongs to the project, app or build its labels name
// (EXC-682). Every volume create must name an owner, and a container mounts a
// volume only when each owner label the volume carries is the container's
// own: an app cannot mount a sibling's disk, nor a project another's.
var OwnerLabels = []string{"excalibase.project", "excalibase.app", "excalibase.build"}

// SharedReadOnlyLabel marks a platform-owned volume, such as the readiness probe's, that any app may
// mount read-only and none may write. SharedFillLabel on a container lets it mount such a volume
// writable, which the platform does once, to fill it, in a container it never starts.
const (
	SharedReadOnlyLabel = "excalibase.shared-readonly"
	SharedFillLabel     = "excalibase.shared-fill"
)

// requireVolumeOwner refuses a volume that names no owner.
func requireVolumeOwner(raw any) error {
	labels, _ := raw.(map[string]any)
	if shared, _ := labels[SharedReadOnlyLabel].(string); shared == "true" {
		return nil
	}
	for _, key := range OwnerLabels {
		if value, _ := labels[key].(string); value != "" {
			return nil
		}
	}
	return refuse("a volume must name its owner in one of %s", strings.Join(OwnerLabels, ", "))
}

// createVolumes is the part of a container create that names volumes.
type createVolumes struct {
	Labels     map[string]string
	HostConfig struct {
		UsernsMode string
		Binds      []string
		Mounts     []struct {
			Type     string
			Source   string
			ReadOnly bool
		}
	}
}

// namedVolume is one named volume a create mounts, and whether it may be written through that mount.
type namedVolume struct {
	name     string
	readOnly bool
}

// named lists the named volumes the create mounts, binds first.
func (c createVolumes) named() []namedVolume {
	var named []namedVolume
	for _, bind := range c.HostConfig.Binds {
		if source, _, _ := strings.Cut(bind, ":"); source != "" {
			named = append(named, namedVolume{name: source})
		}
	}
	for _, mount := range c.HostConfig.Mounts {
		if mount.Type == "volume" && mount.Source != "" {
			named = append(named, namedVolume{name: mount.Source, readOnly: mount.ReadOnly})
		}
	}
	return named
}

// requireOwnVolumes refuses a create that mounts a volume which does not
// exist, is not managed, or belongs to someone else. The engine would create
// a missing named volume unlabelled, so it is refused rather than created.
func (h *Handler) requireOwnVolumes(ctx context.Context, create createVolumes) error {
	for _, mounted := range create.named() {
		name := mounted.name
		var volume struct{ Labels map[string]string }
		err := h.inspect(ctx, "/volumes/"+url.PathEscape(name), &volume)
		if errors.Is(err, errNoSuchThing) {
			return refuse("volume %q does not exist; create it first", name)
		}
		if err != nil {
			return err
		}
		if volume.Labels[h.policy.ManagedLabel] != "true" {
			return refuse("volume %q is not one provisioning manages", name)
		}
		if volume.Labels[SharedReadOnlyLabel] == "true" {
			if err := CheckSharedMount(name, mounted.readOnly, create.Labels); err != nil {
				return err
			}
			continue
		}
		if err := CheckVolumeOwner(name, VolumeOwners(h.policy.VolumePrefix, name, volume.Labels), create.Labels); err != nil {
			return err
		}
		if err := checkVolumeRange(name, volume.Labels, create.HostConfig.UsernsMode); err != nil {
			return err
		}
	}
	return nil
}

// checkVolumeRange: a volume that carries its app's user range is mounted only
// by a container in exactly that range, and one that carries none only by a
// container in none, so an app is never put where another's files are owned.
func checkVolumeRange(name string, labels map[string]string, mode string) error {
	want := ""
	if label := labels[UsernsLabel]; label != "" {
		want = UsernsModeOf(label)
	}
	if mode != want {
		return refuse("volume %q is owned in user range %q; this container runs in %q", name, want, mode)
	}
	return nil
}

// legacyDatabaseSuffix ends the name of a project's MySQL data volume,
// <prefix><project>-mysql, which v1.6 created labelled managed and nothing else.
const legacyDatabaseSuffix = "-mysql"

// VolumeOwners is the owner labels a volume names. A managed volume that names
// none and is called <prefix><project>-mysql is a v1.6 MySQL data volume: the
// engine never adds a label to an existing volume, so its owner is the project
// its name encodes, the one label provisioning now creates it with. The name
// maps to exactly one project, so it admits exactly the containers the
// labelled volume would.
func VolumeOwners(prefix, name string, labels map[string]string) map[string]string {
	owners := map[string]string{}
	for _, key := range OwnerLabels {
		if value := labels[key]; value != "" {
			owners[key] = value
		}
	}
	if len(owners) > 0 || labels[ManagedLabel] != "true" || prefix == "" {
		return owners
	}
	rest, prefixed := strings.CutPrefix(name, prefix)
	project, suffixed := strings.CutSuffix(rest, legacyDatabaseSuffix)
	if prefixed && suffixed && project != "" {
		owners[OwnerLabels[0]] = project
	}
	return owners
}

// CheckVolumeOwner admits a container carrying every owner label the volume does.
func CheckVolumeOwner(name string, volume, container map[string]string) error {
	owned := false
	for _, key := range OwnerLabels {
		owner := volume[key]
		if owner == "" {
			continue
		}
		owned = true
		if container[key] != owner {
			return refuse("volume %q belongs to %s=%s; this container is not it", name, key, owner)
		}
	}
	if !owned {
		return refuse("volume %q names no owner", name)
	}
	return nil
}

// CheckSharedMount admits a shared volume read-only, and writable only to the container the
// platform labelled to fill it.
func CheckSharedMount(name string, readOnly bool, container map[string]string) error {
	if readOnly || container[SharedFillLabel] == "true" {
		return nil
	}
	return refuse("volume %q is shared and read-only; only its filler may write it", name)
}
