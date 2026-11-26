package imagedigest

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"

	"github.com/excalibase/provisioning-poc/internal/apphost"
	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"oras.land/oras-go/v2/content"
)

// maxImageDocument bounds a manifest or config read from a registry we do not control.
const maxImageDocument = 4 << 20

// Volumes are the VOLUME paths an image declares, sorted. A Kubernetes pod mounts them as
// directories on the node's disk whatever the root is, so a deploy caps each (EXC-732). An index is
// followed to its first Linux image: its architectures share a Dockerfile.
func (r *Resolver) Volumes(ctx context.Context, image string, cred *apphost.RegistryCredential) ([]string, error) {
	repo, reference, err := r.repository(image, cred)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, resolveTimeout)
	defer cancel()
	fetch := func(desc ocispec.Descriptor) ([]byte, error) {
		if desc.Size < 0 || desc.Size > maxImageDocument {
			return nil, fmt.Errorf("%w: %s sent a document of %d bytes", ErrUnavailable, apphost.ImageRegistry(image), desc.Size)
		}
		body, err := content.FetchAll(ctx, repo, desc)
		if err != nil {
			return nil, classify(image, err)
		}
		return body, nil
	}
	desc, err := repo.Resolve(ctx, reference)
	if err != nil {
		return nil, classify(image, err)
	}
	manifest, err := r.manifestOf(desc, fetch, image)
	if err != nil {
		return nil, err
	}
	raw, err := fetch(manifest.Config)
	if err != nil {
		return nil, err
	}
	var config struct {
		Config struct {
			Volumes map[string]json.RawMessage `json:"Volumes"`
		} `json:"config"`
	}
	if err := json.Unmarshal(raw, &config); err != nil {
		return nil, fmt.Errorf("%w: %s has a config that cannot be read", ErrUnavailable, image)
	}
	paths := make([]string, 0, len(config.Config.Volumes))
	for path := range config.Config.Volumes {
		paths = append(paths, path)
	}
	slices.Sort(paths)
	return paths, nil
}

// manifestOf reads the image manifest desc names, going through an index to its first Linux entry.
func (r *Resolver) manifestOf(desc ocispec.Descriptor, fetch func(ocispec.Descriptor) ([]byte, error), image string) (ocispec.Manifest, error) {
	for depth := 0; depth < 2; depth++ {
		raw, err := fetch(desc)
		if err != nil {
			return ocispec.Manifest{}, err
		}
		if desc.MediaType == ocispec.MediaTypeImageIndex || desc.MediaType == "application/vnd.docker.distribution.manifest.list.v2+json" {
			var index ocispec.Index
			if err := json.Unmarshal(raw, &index); err != nil {
				return ocispec.Manifest{}, fmt.Errorf("%w: %s has an index that cannot be read", ErrUnavailable, image)
			}
			next := slices.IndexFunc(index.Manifests, func(entry ocispec.Descriptor) bool {
				return entry.Platform != nil && entry.Platform.OS == "linux" && digest.Digest(entry.Digest).Validate() == nil
			})
			if next < 0 {
				return ocispec.Manifest{}, fmt.Errorf("%w: %s has no Linux image", ErrNotFound, image)
			}
			desc = index.Manifests[next]
			continue
		}
		var manifest ocispec.Manifest
		if err := json.Unmarshal(raw, &manifest); err != nil {
			return ocispec.Manifest{}, fmt.Errorf("%w: %s has a manifest that cannot be read", ErrUnavailable, image)
		}
		return manifest, nil
	}
	return ocispec.Manifest{}, fmt.Errorf("%w: %s nests its index too deep", ErrUnavailable, image)
}
