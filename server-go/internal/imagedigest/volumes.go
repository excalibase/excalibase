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

// maxIndexEntries bounds the Linux images one index may make a deploy read.
const maxIndexEntries = 16

// Volumes are the VOLUME paths an image declares, sorted, and the digest of the image they were
// read from, so the deploy can run exactly that. A Kubernetes pod mounts them as directories on
// the node's disk whatever the root is, so a deploy caps each (EXC-732). An index may declare
// different volumes per architecture and the node picks its own, so the answer is the union over
// every Linux image in it; an index with more than maxIndexEntries of them is refused.
func (r *Resolver) Volumes(ctx context.Context, image string, cred *apphost.RegistryCredential) ([]string, string, error) {
	repo, reference, err := r.repositoryVia(image, cred, true)
	if err != nil {
		return nil, "", err
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
	top, err := repo.Resolve(ctx, reference)
	if err != nil {
		return nil, "", classify(image, err)
	}
	manifests, err := linuxManifests(top, fetch, image)
	if err != nil {
		return nil, "", err
	}
	union := map[string]bool{}
	for _, manifest := range manifests {
		raw, err := fetch(manifest.Config)
		if err != nil {
			return nil, "", err
		}
		var config struct {
			Config struct {
				Volumes map[string]json.RawMessage `json:"Volumes"`
			} `json:"config"`
		}
		if err := json.Unmarshal(raw, &config); err != nil {
			return nil, "", fmt.Errorf("%w: %s has a config that cannot be read", ErrUnavailable, image)
		}
		for path := range config.Config.Volumes {
			union[path] = true
		}
	}
	paths := make([]string, 0, len(union))
	for path := range union {
		paths = append(paths, path)
	}
	slices.Sort(paths)
	return paths, top.Digest.String(), nil
}

func isIndex(mediaType string) bool {
	return mediaType == ocispec.MediaTypeImageIndex || mediaType == "application/vnd.docker.distribution.manifest.list.v2+json"
}

// linuxManifests are the manifests top names: itself, or every Linux image of its index.
func linuxManifests(top ocispec.Descriptor, fetch func(ocispec.Descriptor) ([]byte, error), image string) ([]ocispec.Manifest, error) {
	raw, err := fetch(top)
	if err != nil {
		return nil, err
	}
	if !isIndex(top.MediaType) {
		var manifest ocispec.Manifest
		if err := json.Unmarshal(raw, &manifest); err != nil {
			return nil, fmt.Errorf("%w: %s has a manifest that cannot be read", ErrUnavailable, image)
		}
		return []ocispec.Manifest{manifest}, nil
	}
	var index ocispec.Index
	if err := json.Unmarshal(raw, &index); err != nil {
		return nil, fmt.Errorf("%w: %s has an index that cannot be read", ErrUnavailable, image)
	}
	var linux []ocispec.Descriptor
	for _, entry := range index.Manifests {
		if entry.Platform != nil && entry.Platform.OS == "linux" && digest.Digest(entry.Digest).Validate() == nil {
			linux = append(linux, entry)
		}
	}
	if len(linux) == 0 {
		return nil, fmt.Errorf("%w: %s has no Linux image", ErrNotFound, image)
	}
	if len(linux) > maxIndexEntries {
		return nil, fmt.Errorf("%w: %s lists %d Linux images, more than the %d read", ErrUnavailable, image, len(linux), maxIndexEntries)
	}
	manifests := make([]ocispec.Manifest, 0, len(linux))
	for _, entry := range linux {
		if isIndex(entry.MediaType) {
			return nil, fmt.Errorf("%w: %s nests an index inside its index", ErrUnavailable, image)
		}
		body, err := fetch(entry)
		if err != nil {
			return nil, err
		}
		var manifest ocispec.Manifest
		if err := json.Unmarshal(body, &manifest); err != nil {
			return nil, fmt.Errorf("%w: %s has a manifest that cannot be read", ErrUnavailable, image)
		}
		manifests = append(manifests, manifest)
	}
	return manifests, nil
}
