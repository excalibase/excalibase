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

// requireVolumeOwner refuses a volume that names no owner.
func requireVolumeOwner(raw any) error {
	labels, _ := raw.(map[string]any)
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
		Binds  []string
		Mounts []struct {
			Type   string
			Source string
		}
	}
}

// named lists the named volumes the create mounts, binds first.
func (c createVolumes) named() []string {
	var names []string
	for _, bind := range c.HostConfig.Binds {
		if source, _, _ := strings.Cut(bind, ":"); source != "" {
			names = append(names, source)
		}
	}
	for _, mount := range c.HostConfig.Mounts {
		if mount.Type == "volume" && mount.Source != "" {
			names = append(names, mount.Source)
		}
	}
	return names
}

// requireOwnVolumes refuses a create that mounts a volume which does not
// exist, is not managed, or belongs to someone else. The engine would create
// a missing named volume unlabelled, so it is refused rather than created.
func (h *Handler) requireOwnVolumes(ctx context.Context, create createVolumes) error {
	for _, name := range create.named() {
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
		if err := CheckVolumeOwner(name, volume.Labels, create.Labels); err != nil {
			return err
		}
	}
	return nil
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
