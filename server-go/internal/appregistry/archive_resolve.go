package appregistry

import (
	"encoding/json"
	"fmt"
	"io"

	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

// Media types an image the platform builds may hold; nothing points outside the archive.
var (
	manifestTypes = map[string]bool{ocispec.MediaTypeImageManifest: true, "application/vnd.docker.distribution.manifest.v2+json": true}
	indexTypes    = map[string]bool{ocispec.MediaTypeImageIndex: true, "application/vnd.docker.distribution.manifest.list.v2+json": true}
)

// resolve reads index.json: exactly one image, which is a manifest or an
// index of manifests, every blob it names present at its stated size.
func (l *Layout) resolve(raw []byte) error {
	var index ocispec.Index
	if err := json.Unmarshal(raw, &index); err != nil || index.SchemaVersion != 2 || len(index.Manifests) != 1 {
		return fmt.Errorf("%w: index.json must name exactly one image", ErrBadArchive)
	}
	l.Root = index.Manifests[0]
	if err := l.present(l.Root); err != nil {
		return err
	}
	seen := map[string]bool{}
	switch {
	case manifestTypes[l.Root.MediaType]:
		return l.addManifest(l.Root, seen)
	case indexTypes[l.Root.MediaType]:
		var nested ocispec.Index
		if err := l.readJSON(l.Root, &nested); err != nil {
			return err
		}
		if nested.MediaType != "" && nested.MediaType != l.Root.MediaType || len(nested.Manifests) == 0 {
			return fmt.Errorf("%w: the image index names no manifests", ErrBadArchive)
		}
		for _, child := range nested.Manifests {
			if !manifestTypes[child.MediaType] {
				return fmt.Errorf("%w: an index inside an index", ErrBadArchive)
			}
			if err := l.present(child); err != nil {
				return err
			}
			l.Children = append(l.Children, child)
			if err := l.addManifest(child, seen); err != nil {
				return err
			}
		}
		return nil
	}
	return fmt.Errorf("%w: index.json names a %q", ErrBadArchive, l.Root.MediaType)
}

func (l *Layout) addManifest(desc ocispec.Descriptor, seen map[string]bool) error {
	var manifest ocispec.Manifest
	if err := l.readJSON(desc, &manifest); err != nil {
		return err
	}
	if manifest.SchemaVersion != 2 || manifest.MediaType != "" && manifest.MediaType != desc.MediaType {
		return fmt.Errorf("%w: manifest %s is not what index.json says", ErrBadArchive, desc.Digest)
	}
	for _, blob := range append([]ocispec.Descriptor{manifest.Config}, manifest.Layers...) {
		if err := l.present(blob); err != nil {
			return err
		}
		if !seen[string(blob.Digest)] {
			seen[string(blob.Digest)] = true
			l.Blobs = append(l.Blobs, blob)
		}
	}
	return nil
}

// present holds a descriptor to a blob in the archive at its stated size, with no foreign URLs.
func (l *Layout) present(desc ocispec.Descriptor) error {
	size, ok := l.blobs[string(desc.Digest)]
	if !ok || size != desc.Size {
		return fmt.Errorf("%w: %s is not in the archive at %d bytes", ErrBadArchive, desc.Digest, desc.Size)
	}
	if len(desc.URLs) > 0 {
		return fmt.Errorf("%w: %s points outside the archive", ErrBadArchive, desc.Digest)
	}
	return nil
}

func (l *Layout) readJSON(desc ocispec.Descriptor, into any) error {
	if desc.Size > maxManifest {
		return fmt.Errorf("%w: manifest %s over %d bytes", ErrBadArchive, desc.Digest, maxManifest)
	}
	file, err := l.Open(desc)
	if err != nil {
		return err
	}
	defer file.Close()
	body, err := io.ReadAll(io.LimitReader(file, maxManifest))
	if err != nil {
		return err
	}
	if err := json.Unmarshal(body, into); err != nil {
		return fmt.Errorf("%w: %s is not JSON", ErrBadArchive, desc.Digest)
	}
	return nil
}
