package config

import (
	"strings"
	"testing"

	"sigs.k8s.io/yaml"
)

// The ClusterImageCatalog is generated from the same catalogue provisioning
// validates against, so the Kubernetes object and the Go code cannot disagree.
// These tests pin that relationship.

func decodeCatalogObject(t *testing.T, raw []byte) map[string]interface{} {
	t.Helper()
	var obj map[string]interface{}
	if err := yaml.Unmarshal(raw, &obj); err != nil {
		t.Fatalf("generated object is not valid YAML: %v", err)
	}
	return obj
}

func TestClusterImageCatalogIsARealCNPGObject(t *testing.T) {
	raw, err := RenderClusterImageCatalog()
	if err != nil {
		t.Skipf("no published majors yet: %v", err)
	}
	obj := decodeCatalogObject(t, raw)
	if obj["apiVersion"] != "postgresql.cnpg.io/v1" {
		t.Errorf("apiVersion: got %v", obj["apiVersion"])
	}
	if obj["kind"] != "ClusterImageCatalog" {
		t.Errorf("kind: got %v", obj["kind"])
	}
}

func TestClusterImageCatalogListsEveryPublishedMajorAndNothingElse(t *testing.T) {
	raw, err := RenderClusterImageCatalog()
	if err != nil {
		t.Skipf("no published majors yet: %v", err)
	}
	obj := decodeCatalogObject(t, raw)
	spec, _ := obj["spec"].(map[string]interface{})
	images, _ := spec["images"].([]interface{})

	rendered := map[string]string{}
	for _, raw := range images {
		entry, _ := raw.(map[string]interface{})
		major, _ := entry["major"].(float64)
		image, _ := entry["image"].(string)
		rendered[strings.TrimSpace(formatMajor(int(major)))] = image
	}

	for _, entry := range PostgresCatalogEntries() {
		if entry.Image == "" {
			if _, present := rendered[entry.Major]; present {
				t.Errorf("major %s is unpublished but was rendered", entry.Major)
			}
			continue
		}
		// The rendered reference is the one provisioning resolves, tag and
		// digest together — not the bare digest the file records.
		want, err := PostgresImage(entry.Major)
		if err != nil {
			t.Errorf("major %s: %v", entry.Major, err)
			continue
		}
		if rendered[entry.Major] != want {
			t.Errorf("major %s: rendered %q, want %q", entry.Major, rendered[entry.Major], want)
		}
		delete(rendered, entry.Major)
	}
	for major := range rendered {
		t.Errorf("rendered major %s is not in the catalogue", major)
	}
}

// Rendering must fail rather than emit an empty or partially pinned catalogue:
// an empty ClusterImageCatalog would silently match nothing.
func TestRenderClusterImageCatalogFailsWhenNothingIsPublished(t *testing.T) {
	_, err := renderClusterImageCatalog(PostgresCatalog{
		Majors: []PostgresMajorEntry{{Major: "17", BaseImage: "ghcr.io/x/y@sha256:aa"}},
	})
	if err == nil {
		t.Fatal("rendering with no published image must fail")
	}
}

func TestRenderClusterImageCatalogRejectsANonNumericMajor(t *testing.T) {
	_, err := renderClusterImageCatalog(PostgresCatalog{
		Majors: []PostgresMajorEntry{{Major: "seventeen", BaseImage: "ghcr.io/x/y@sha256:aa", Image: "ghcr.io/x/y@sha256:bb"}},
	})
	if err == nil {
		t.Fatal("CNPG's major field is an integer; a non-numeric major must fail")
	}
}

func TestRenderClusterImageCatalogEmitsIntegerMajors(t *testing.T) {
	raw, err := renderClusterImageCatalog(PostgresCatalog{
		Majors: []PostgresMajorEntry{{Major: "17", BaseImage: "ghcr.io/x/y@sha256:aa", Image: "ghcr.io/x/y@sha256:bb"}},
	})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if !strings.Contains(string(raw), "major: 17") {
		t.Errorf("major must render as an integer, got:\n%s", raw)
	}
	if strings.Contains(string(raw), `major: "17"`) {
		t.Errorf("major rendered as a string, got:\n%s", raw)
	}
}

func TestRenderClusterImageCatalogRecordsDocumentDBMajors(t *testing.T) {
	raw, err := renderClusterImageCatalog(PostgresCatalog{
		DocumentDBRef: "v0.117-0",
		Majors: []PostgresMajorEntry{
			{Major: "16", BaseImage: "ghcr.io/x/y@sha256:aa", Image: "ghcr.io/x/y@sha256:bb", DocumentDB: true},
			{Major: "15", BaseImage: "ghcr.io/x/y@sha256:cc", Image: "ghcr.io/x/y@sha256:dd"},
		},
	})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	obj := decodeCatalogObject(t, raw)
	meta, _ := obj["metadata"].(map[string]interface{})
	annotations, _ := meta["annotations"].(map[string]interface{})
	if annotations[documentDBMajorsAnnotation] != "16" {
		t.Errorf("%s: got %v, want %q", documentDBMajorsAnnotation, annotations[documentDBMajorsAnnotation], "16")
	}
	if annotations[documentDBRefAnnotation] != "v0.117-0" {
		t.Errorf("%s: got %v", documentDBRefAnnotation, annotations[documentDBRefAnnotation])
	}
}

func TestBuildMatrixCoversEveryMajorAndCarriesTheDocumentDBRef(t *testing.T) {
	raw, err := RenderBuildMatrix()
	if err != nil {
		t.Fatalf("RenderBuildMatrix: %v", err)
	}
	var rows []BuildMatrixEntry
	if err := yaml.Unmarshal(raw, &rows); err != nil {
		t.Fatalf("matrix is not valid JSON: %v", err)
	}

	entries := PostgresCatalogEntries()
	if len(rows) != len(entries) {
		t.Fatalf("matrix has %d rows, catalogue has %d majors", len(rows), len(entries))
	}
	for i, entry := range entries {
		if rows[i].Major != entry.Major {
			t.Errorf("row %d: major %q, want %q", i, rows[i].Major, entry.Major)
		}
		if rows[i].Base != entry.BaseImage {
			t.Errorf("major %s: base %q, want %q", entry.Major, rows[i].Base, entry.BaseImage)
		}
		want := ""
		if entry.DocumentDB {
			want = DocumentDBRef()
		}
		if rows[i].DocumentDBRef != want {
			t.Errorf("major %s: documentdbRef %q, want %q", entry.Major, rows[i].DocumentDBRef, want)
		}
	}
}

func TestFormatMajorRendersTheBareNumber(t *testing.T) {
	if got := formatMajor(17); got != "17" {
		t.Errorf("got %q, want %q", got, "17")
	}
}
