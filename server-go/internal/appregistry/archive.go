package appregistry

import (
	"archive/tar"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"

	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

const (
	// maxLayoutFile caps index.json and oci-layout, read into memory.
	maxLayoutFile = 1 << 20
	// maxManifest caps a manifest or index blob, read into memory.
	maxManifest = 4 << 20
	// maxArchiveEntries caps the files an archive may hold.
	maxArchiveEntries = 10_000
)

// ErrBadArchive refuses an image archive the build wrote: the build ran the
// customer's code, so nothing in its output is trusted until it is checked.
var ErrBadArchive = errors.New("the build's image archive is not a single OCI image")

var blobPath = regexp.MustCompile(`^blobs/sha256/([0-9a-f]{64})$`)

// Layout is an OCI image layout taken from an archive: every blob was written
// under the name of the digest its bytes hash to, and the image index.json
// names references only blobs that are there.
type Layout struct {
	dir   string
	blobs map[string]int64
	// Root is the one image the archive holds: a manifest, or an index of manifests.
	Root ocispec.Descriptor
	// Children are the manifests an index root names, in its order.
	Children []ocispec.Descriptor
	// Blobs are the configs and layers, each once.
	Blobs []ocispec.Descriptor
}

// ReadLayout reads an OCI layout tar from r into dir, at most limit bytes of
// file content. Links, devices, paths outside the layout and blobs whose
// bytes do not hash to their name are refused.
func ReadLayout(r io.Reader, dir string, limit int64) (*Layout, error) {
	layout := &Layout{dir: dir, blobs: map[string]int64{}}
	var index []byte
	sawLayout := false
	tr := tar.NewReader(r)
	var total int64
	for entries := 0; ; entries++ {
		header, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("%w: %v", ErrBadArchive, err)
		}
		if entries >= maxArchiveEntries {
			return nil, fmt.Errorf("%w: more than %d files", ErrBadArchive, maxArchiveEntries)
		}
		name, err := entryName(header)
		if err != nil || name == "" {
			if err != nil {
				return nil, err
			}
			continue
		}
		if total += header.Size; total > limit {
			return nil, fmt.Errorf("%w: larger than %d bytes", ErrBadArchive, limit)
		}
		switch {
		case name == "index.json":
			if index != nil {
				return nil, fmt.Errorf("%w: two index.json", ErrBadArchive)
			}
			if index, err = readSmall(tr, maxLayoutFile); err != nil {
				return nil, err
			}
		case name == "oci-layout":
			if sawLayout, err = true, checkLayoutFile(tr); err != nil {
				return nil, err
			}
		case blobPath.MatchString(name):
			if err := layout.writeBlob(tr, blobPath.FindStringSubmatch(name)[1], header.Size); err != nil {
				return nil, err
			}
		default:
			// Anything else (BuildKit's manifest.json) is not read.
			if _, err := io.Copy(io.Discard, tr); err != nil {
				return nil, fmt.Errorf("%w: %v", ErrBadArchive, err)
			}
		}
	}
	if index == nil || !sawLayout {
		return nil, fmt.Errorf("%w: no index.json or oci-layout", ErrBadArchive)
	}
	if err := layout.resolve(index); err != nil {
		return nil, err
	}
	return layout, nil
}

// entryName answers the layout path of a regular file, "" for a directory.
func entryName(header *tar.Header) (string, error) {
	name := strings.TrimPrefix(header.Name, "./")
	switch header.Typeflag {
	case tar.TypeDir:
		return "", nil
	case tar.TypeReg:
	default:
		return "", fmt.Errorf("%w: %q is a link or a device", ErrBadArchive, header.Name)
	}
	if name == "" || path.IsAbs(name) || strings.Contains(name, `\`) || path.Clean(name) != name || strings.HasPrefix(name, "../") || name == ".." {
		return "", fmt.Errorf("%w: %q is outside the layout", ErrBadArchive, header.Name)
	}
	if header.Size < 0 {
		return "", fmt.Errorf("%w: %q has a negative size", ErrBadArchive, header.Name)
	}
	return name, nil
}

func readSmall(r io.Reader, limit int64) ([]byte, error) {
	body, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrBadArchive, err)
	}
	if int64(len(body)) > limit {
		return nil, fmt.Errorf("%w: a layout file over %d bytes", ErrBadArchive, limit)
	}
	return body, nil
}

func checkLayoutFile(r io.Reader) error {
	body, err := readSmall(r, maxLayoutFile)
	if err != nil {
		return err
	}
	var file ocispec.ImageLayout
	if json.Unmarshal(body, &file) != nil || file.Version != ocispec.ImageLayoutVersion {
		return fmt.Errorf("%w: oci-layout is not version %s", ErrBadArchive, ocispec.ImageLayoutVersion)
	}
	return nil
}

// writeBlob stores a blob under its hex digest, refusing bytes that hash to anything else.
func (l *Layout) writeBlob(r io.Reader, hexDigest string, size int64) error {
	if _, dup := l.blobs["sha256:"+hexDigest]; dup {
		return fmt.Errorf("%w: blob %s twice", ErrBadArchive, hexDigest)
	}
	file, err := os.OpenFile(filepath.Join(l.dir, hexDigest), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	hash := sha256.New()
	written, copyErr := io.Copy(io.MultiWriter(file, hash), r)
	if closeErr := file.Close(); copyErr == nil {
		copyErr = closeErr
	}
	if copyErr != nil {
		return fmt.Errorf("%w: %v", ErrBadArchive, copyErr)
	}
	if written != size || hex.EncodeToString(hash.Sum(nil)) != hexDigest {
		return fmt.Errorf("%w: blob %s does not hash to its name", ErrBadArchive, hexDigest)
	}
	l.blobs["sha256:"+hexDigest] = size
	return nil
}

// Open reads a blob the layout holds.
func (l *Layout) Open(desc ocispec.Descriptor) (*os.File, error) {
	if _, ok := l.blobs[string(desc.Digest)]; !ok {
		return nil, fmt.Errorf("%w: no blob %s", ErrBadArchive, desc.Digest)
	}
	return os.Open(filepath.Join(l.dir, strings.TrimPrefix(string(desc.Digest), "sha256:")))
}

// Contents is every blob and manifest the image holds, by digest and size.
func (l *Layout) Contents() map[string]int64 {
	contents := map[string]int64{}
	for _, desc := range append(append([]ocispec.Descriptor{l.Root}, l.Children...), l.Blobs...) {
		contents[string(desc.Digest)] = desc.Size
	}
	return contents
}

// Size is the bytes the image holds, each blob counted once.
func (l *Layout) Size() int64 {
	var total int64
	for _, size := range l.Contents() {
		total += size
	}
	return total
}
