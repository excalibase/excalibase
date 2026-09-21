package config

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"sigs.k8s.io/yaml"
)

// The CNPG ClusterImageCatalog is generated from the catalogue provisioning
// validates against rather than hand-maintained next to it. One source of
// truth means the Kubernetes object and the Go code cannot silently disagree.

const (
	clusterImageCatalogName    = "excalibase-postgresql"
	documentDBMajorsAnnotation = "postgres.excalibase.io/documentdb-majors"
	// The CNPG CRD has no field for this, so the majors that can offer
	// DocumentDB — and the upstream tag their images compile — travel as
	// annotations. Nothing reads them back; the Go catalogue stays
	// authoritative. They are there so an operator reading the cluster can see
	// what the images carry and which DocumentDB it is.
	documentDBRefAnnotation = "postgres.excalibase.io/documentdb-ref"
)

func formatMajor(major int) string { return strconv.Itoa(major) }

// RenderClusterImageCatalog renders the CNPG ClusterImageCatalog for the
// compiled-in catalogue.
func RenderClusterImageCatalog() ([]byte, error) {
	return renderClusterImageCatalog(postgresCatalog)
}

func renderClusterImageCatalog(catalog PostgresCatalog) ([]byte, error) {
	images := make([]map[string]interface{}, 0, len(catalog.Majors))
	documentDBMajors := make([]string, 0, len(catalog.Majors))

	for _, entry := range catalog.Majors {
		major, err := strconv.Atoi(entry.Major)
		if err != nil {
			return nil, fmt.Errorf("major %q is not an integer: %w", entry.Major, err)
		}
		if entry.Image == "" {
			// Unpublished majors are left out on purpose. Emitting them with
			// a placeholder would hand CNPG an image reference that cannot be
			// pulled, which fails later and less clearly.
			continue
		}
		images = append(images, map[string]interface{}{"major": major, "image": taggedImageReference(entry)})
		if entry.DocumentDB {
			documentDBMajors = append(documentDBMajors, entry.Major)
		}
	}

	if len(images) == 0 {
		return nil, fmt.Errorf("no major has a published image; nothing to put in a ClusterImageCatalog")
	}

	annotations := map[string]string{
		documentDBMajorsAnnotation: strings.Join(documentDBMajors, ","),
	}
	if catalog.DocumentDBRef != "" {
		annotations[documentDBRefAnnotation] = catalog.DocumentDBRef
	}

	object := map[string]interface{}{
		"apiVersion": "postgresql.cnpg.io/v1",
		"kind":       "ClusterImageCatalog",
		"metadata": map[string]interface{}{
			"name":        clusterImageCatalogName,
			"annotations": annotations,
		},
		"spec": map[string]interface{}{"images": images},
	}

	out, err := yaml.Marshal(object)
	if err != nil {
		return nil, fmt.Errorf("marshal ClusterImageCatalog: %w", err)
	}
	header := "# Generated from server-go/internal/config/postgres_catalog.yaml.\n" +
		"# Do not edit by hand: regenerate with `go run ./cmd/pgcatalog`.\n"
	return append([]byte(header), out...), nil
}

// BuildMatrixEntry is one row of the image publish workflow's matrix.
type BuildMatrixEntry struct {
	Major string `json:"major"`
	Base  string `json:"base"`
	// DocumentDBRef is the upstream tag to compile for majors whose image
	// carries DocumentDB, and empty for the rest. The Dockerfile reads it that
	// way: empty means "skip the DocumentDB stage".
	DocumentDBRef string `json:"documentdbRef"`
	// Image is what the catalogue pins, empty when unpublished. The workflow
	// skips a published major: rebuilding pushes a new digest and its own
	// pinning gate then fails (EXC-433).
	Image string `json:"image,omitempty"`
}

// RenderBuildMatrix renders the image publish workflow's matrix as JSON. The
// workflow builds one image per entry, so a major that is not in the catalogue
// is never built and a major that is cannot be forgotten.
func RenderBuildMatrix() ([]byte, error) {
	return renderBuildMatrix(postgresCatalog)
}

func renderBuildMatrix(catalog PostgresCatalog) ([]byte, error) {
	entries := make([]BuildMatrixEntry, 0, len(catalog.Majors))
	for _, entry := range catalog.Majors {
		row := BuildMatrixEntry{Major: entry.Major, Base: entry.BaseImage, Image: entry.Image}
		if entry.DocumentDB {
			row.DocumentDBRef = catalog.DocumentDBRef
		}
		entries = append(entries, row)
	}
	out, err := json.Marshal(entries)
	if err != nil {
		return nil, fmt.Errorf("marshal build matrix: %w", err)
	}
	return append(out, '\n'), nil
}
