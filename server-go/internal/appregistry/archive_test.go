package appregistry

import (
	"archive/tar"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"slices"
	"strings"
	"syscall"
	"testing"
	"testing/iotest"

	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

type entry struct {
	name     string
	body     []byte
	typeflag byte
}

func tarOf(t *testing.T, entries ...entry) []byte {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for _, e := range entries {
		flag := e.typeflag
		if flag == 0 {
			flag = tar.TypeReg
		}
		header := &tar.Header{Name: e.name, Typeflag: flag, Mode: 0o644, Size: int64(len(e.body))}
		if flag != tar.TypeReg {
			header.Size, header.Linkname = 0, "/etc/passwd"
		}
		if err := tw.WriteHeader(header); err != nil {
			t.Fatal(err)
		}
		if flag == tar.TypeReg {
			_, _ = tw.Write(e.body)
		}
	}
	_ = tw.Close()
	return buf.Bytes()
}

func blob(body []byte) (entry, ocispec.Descriptor) {
	sum := sha256.Sum256(body)
	hexDigest := hex.EncodeToString(sum[:])
	return entry{name: "blobs/sha256/" + hexDigest, body: body}, ocispec.Descriptor{Digest: digest.Digest("sha256:" + hexDigest), Size: int64(len(body))}
}

func jsonOf(t *testing.T, v any) []byte {
	t.Helper()
	body, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return body
}

var layoutFile = entry{name: "oci-layout", body: []byte(`{"imageLayoutVersion":"1.0.0"}`)}

// image is a one-layer OCI image: its entries and the manifest's descriptor.
func image(t *testing.T, arch string, mutate func(*ocispec.Manifest)) ([]entry, ocispec.Descriptor) {
	t.Helper()
	configEntry, config := blob([]byte(`{"architecture":"` + arch + `","os":"linux"}`))
	config.MediaType = ocispec.MediaTypeImageConfig
	layerEntry, layer := blob([]byte("layer bytes shared by every image"))
	layer.MediaType = ocispec.MediaTypeImageLayerGzip
	manifest := ocispec.Manifest{MediaType: ocispec.MediaTypeImageManifest, Config: config, Layers: []ocispec.Descriptor{layer}}
	manifest.SchemaVersion = 2
	if mutate != nil {
		mutate(&manifest)
	}
	manifestEntry, desc := blob(jsonOf(t, manifest))
	desc.MediaType = ocispec.MediaTypeImageManifest
	return []entry{configEntry, layerEntry, manifestEntry}, desc
}

func indexEntry(t *testing.T, descs ...ocispec.Descriptor) entry {
	t.Helper()
	index := ocispec.Index{Manifests: descs}
	index.SchemaVersion = 2
	return entry{name: "index.json", body: jsonOf(t, index)}
}

func readLayout(t *testing.T, archive []byte) (*Layout, error) {
	t.Helper()
	return ReadLayout(bytes.NewReader(archive), t.TempDir(), 1<<20)
}

func TestReadLayout_TakesOneImage(t *testing.T) {
	entries, root := image(t, "amd64", nil)
	archive := tarOf(t, append([]entry{{name: "./", typeflag: tar.TypeDir}, layoutFile, {name: "./manifest.json", body: []byte("[]")}, indexEntry(t, root)}, entries...)...)
	layout, err := readLayout(t, archive)
	if err != nil {
		t.Fatal(err)
	}
	if layout.Root.Digest != root.Digest || len(layout.Blobs) != 2 || len(layout.Children) != 0 {
		t.Fatalf("%+v", layout)
	}
	file, err := layout.Open(layout.Blobs[1])
	if err != nil {
		t.Fatal(err)
	}
	_ = file.Close()
	if _, err := layout.Open(ocispec.Descriptor{Digest: "sha256:" + digest.Digest(strings.Repeat("0", 64))}); err == nil {
		t.Error("opened a blob the archive does not hold")
	}
}

func TestReadLayout_TakesAnIndexOfManifests(t *testing.T) {
	amd, amdDesc := image(t, "amd64", nil)
	arm, armDesc := image(t, "arm64", nil)
	indexBody := ocispec.Index{MediaType: ocispec.MediaTypeImageIndex, Manifests: []ocispec.Descriptor{amdDesc, armDesc}}
	indexBody.SchemaVersion = 2
	indexBlob, rootDesc := blob(jsonOf(t, indexBody))
	rootDesc.MediaType = ocispec.MediaTypeImageIndex
	// The layer both images share is in the archive once.
	entries := append(append([]entry{layoutFile, indexEntry(t, rootDesc), indexBlob}, amd...), arm[0], arm[2])
	layout, err := readLayout(t, tarOf(t, entries...))
	if err != nil {
		t.Fatal(err)
	}
	if len(layout.Children) != 2 || len(layout.Blobs) != 3 {
		t.Fatalf("children %d blobs %d: the shared layer counts once", len(layout.Children), len(layout.Blobs))
	}
}

func TestReadLayout_RefusesWhatABuildCouldPlant(t *testing.T) {
	good, root := image(t, "amd64", nil)
	base := slices.Concat([]entry{layoutFile, indexEntry(t, root)}, good)
	badHashEntry, _ := blob([]byte("x"))
	badHashEntry.body = []byte("y")
	foreign, foreignRoot := image(t, "amd64", func(m *ocispec.Manifest) { m.Layers[0].URLs = []string{"http://169.254.169.254/"} })
	missing, missingRoot := image(t, "amd64", func(m *ocispec.Manifest) { m.Layers[0].Size++ })
	nestedIndexBody := ocispec.Index{Manifests: []ocispec.Descriptor{{MediaType: ocispec.MediaTypeImageIndex, Digest: root.Digest, Size: root.Size}}}
	nestedIndexBody.SchemaVersion = 2
	nestedBlob, nestedRoot := blob(jsonOf(t, nestedIndexBody))
	nestedRoot.MediaType = ocispec.MediaTypeImageIndex
	mismatched := root
	mismatched.MediaType = ocispec.MediaTypeImageIndex
	unknown := root
	unknown.MediaType = "application/vnd.example.thing"
	cases := map[string][]entry{
		"a symlink":            slices.Concat(base, []entry{{name: "blobs/sha256/evil", typeflag: tar.TypeSymlink}}),
		"a hard link":          slices.Concat(base, []entry{{name: "x", typeflag: tar.TypeLink}}),
		"a device":             slices.Concat(base, []entry{{name: "x", typeflag: tar.TypeChar}}),
		"a parent path":        slices.Concat(base, []entry{{name: "../escape", body: []byte("x")}}),
		"an absolute path":     slices.Concat(base, []entry{{name: "/etc/cron.d/x", body: []byte("x")}}),
		"an unclean path":      slices.Concat(base, []entry{{name: "blobs/../index.json", body: []byte("x")}}),
		"a bad hash":           slices.Concat(base, []entry{badHashEntry}),
		"a blob twice":         slices.Concat(base, []entry{good[0]}),
		"index.json twice":     slices.Concat(base, []entry{indexEntry(t, root)}),
		"no oci-layout":        slices.Concat([]entry{indexEntry(t, root)}, good),
		"a layout version":     slices.Concat([]entry{{name: "oci-layout", body: []byte(`{"imageLayoutVersion":"9"}`)}, indexEntry(t, root)}, good),
		"two images":           slices.Concat([]entry{layoutFile, indexEntry(t, root, root)}, good),
		"a foreign layer":      slices.Concat([]entry{layoutFile, indexEntry(t, foreignRoot)}, foreign),
		"a wrong size":         slices.Concat([]entry{layoutFile, indexEntry(t, missingRoot)}, missing),
		"a missing blob":       {layoutFile, indexEntry(t, root), good[2]},
		"an index in an index": slices.Concat([]entry{layoutFile, indexEntry(t, nestedRoot), nestedBlob}, good),
		"a manifest as index":  slices.Concat([]entry{layoutFile, indexEntry(t, mismatched)}, good),
		"an unknown type":      slices.Concat([]entry{layoutFile, indexEntry(t, unknown)}, good),
		"not json":             {layoutFile, {name: "index.json", body: []byte("{")}},
		"not a tar":            nil,
	}
	for name, entries := range cases {
		archive := tarOf(t, entries...)
		if entries == nil {
			archive = []byte("this is not a tar archive at all, not even close to one")
		}
		if _, err := readLayout(t, archive); !errors.Is(err, ErrBadArchive) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, err := ReadLayout(bytes.NewReader(tarOf(t, base...)), t.TempDir(), 64); !errors.Is(err, ErrArchiveTooLarge) || !errors.Is(err, ErrBadArchive) {
		t.Errorf("over the limit: %v", err)
	}
}

func TestReadLayout_RefusesAManifestOverItsCap(t *testing.T) {
	configEntry, config := blob([]byte(`{"architecture":"amd64","os":"linux"}`))
	config.MediaType = ocispec.MediaTypeImageConfig
	layerEntry, layer := blob([]byte("layer"))
	layer.MediaType = ocispec.MediaTypeImageLayerGzip
	manifest := ocispec.Manifest{MediaType: ocispec.MediaTypeImageManifest, Config: config, Layers: []ocispec.Descriptor{layer}}
	manifest.SchemaVersion = 2
	// Trailing whitespace keeps it valid JSON even when cut at the cap, so only the cap refuses it.
	padded := append(jsonOf(t, manifest), bytes.Repeat([]byte(" "), maxManifest)...)
	manifestEntry, root := blob(padded)
	root.MediaType = ocispec.MediaTypeImageManifest
	archive := tarOf(t, layoutFile, indexEntry(t, root), configEntry, layerEntry, manifestEntry)
	if _, err := ReadLayout(bytes.NewReader(archive), t.TempDir(), 2*maxManifest); !errors.Is(err, ErrBadArchive) {
		t.Fatalf("a %d-byte manifest: %v", root.Size, err)
	}
}

func TestReadLayout_RefusesMoreEntriesThanItsCap(t *testing.T) {
	good, root := image(t, "amd64", nil)
	dirs := make([]entry, maxArchiveEntries)
	for i := range dirs {
		dirs[i] = entry{name: "./", typeflag: tar.TypeDir}
	}
	archive := tarOf(t, slices.Concat([]entry{layoutFile, indexEntry(t, root)}, good, dirs)...)
	if _, err := readLayout(t, archive); !errors.Is(err, ErrBadArchive) {
		t.Fatalf("%d entries: %v", maxArchiveEntries+len(good)+2, err)
	}
}

// /dev/full answers every write with the kernel's own ENOSPC: a full disk is no fault of the archive.
func TestSaveBlob_AFullDiskIsNotABadArchive(t *testing.T) {
	full, err := os.OpenFile("/dev/full", os.O_WRONLY, 0)
	if err != nil {
		t.Skip("no /dev/full here")
	}
	defer full.Close()
	_, err = saveBlob(full, strings.NewReader("layer"))
	if !errors.Is(err, syscall.ENOSPC) || errors.Is(err, ErrBadArchive) {
		t.Fatalf("%v", err)
	}
}

func TestSaveBlob_AStreamThatBreaksIsABadArchive(t *testing.T) {
	file, err := os.Create(t.TempDir() + "/blob")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	_, err = saveBlob(file, iotest.ErrReader(errors.New("truncated")))
	if !errors.Is(err, ErrBadArchive) {
		t.Fatalf("%v", err)
	}
}
