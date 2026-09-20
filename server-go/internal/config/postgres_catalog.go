package config

import (
	_ "embed"
	"fmt"
	"regexp"
	"strings"

	"sigs.k8s.io/yaml"
)

//go:embed postgres_catalog.yaml
var postgresCatalogYAML []byte

// PostgresMajorEntry is one supported PostgreSQL major and the image that
// serves it.
type PostgresMajorEntry struct {
	// Major is the bare major version, e.g. "17".
	Major string `json:"major"`
	// BaseImage is the upstream CNPG image the Excalibase image is built from,
	// always digest-pinned.
	BaseImage string `json:"baseImage"`
	// Image is the published Excalibase image, digest-pinned. Empty means the
	// major has not been published yet; callers must refuse rather than
	// substitute anything.
	Image string `json:"image,omitempty"`
	// DocumentDB reports whether Image carries the DocumentDB extension.
	DocumentDB bool `json:"documentdb,omitempty"`
}

// PostgresCatalog is the whole catalogue as it appears on disk.
type PostgresCatalog struct {
	// DocumentDBVersion is the upstream release the DocumentDB packages come
	// from, e.g. "0.114-0".
	DocumentDBVersion string               `json:"documentDBVersion,omitempty"`
	Majors            []PostgresMajorEntry `json:"majors"`
}

var postgresCatalog PostgresCatalog

// digestRef matches a fully qualified, digest-pinned image reference. A tag is
// deliberately not accepted: the bits behind a tag can change without anything
// in the platform recording that they did.
var digestRef = regexp.MustCompile(`^[^@:\s]+(:[0-9]+)?/[^@\s]+@sha256:[0-9a-f]{2,64}$`)

func init() {
	catalog, err := parsePostgresCatalog(postgresCatalogYAML)
	if err != nil {
		// The catalogue is compiled in. An unparseable one is a build-time
		// mistake that must never reach a running server.
		panic(fmt.Sprintf("postgres catalog: %v", err))
	}
	postgresCatalog = catalog
}

func parsePostgresCatalog(raw []byte) (PostgresCatalog, error) {
	var catalog PostgresCatalog
	if err := yaml.UnmarshalStrict(raw, &catalog); err != nil {
		return PostgresCatalog{}, fmt.Errorf("parse: %w", err)
	}
	if len(catalog.Majors) == 0 {
		return PostgresCatalog{}, fmt.Errorf("catalogue lists no majors")
	}

	seen := make(map[string]bool, len(catalog.Majors))
	for _, entry := range catalog.Majors {
		if strings.TrimSpace(entry.Major) == "" {
			return PostgresCatalog{}, fmt.Errorf("an entry has an empty major")
		}
		if seen[entry.Major] {
			return PostgresCatalog{}, fmt.Errorf("major %q appears twice", entry.Major)
		}
		seen[entry.Major] = true

		if !digestRef.MatchString(entry.BaseImage) {
			return PostgresCatalog{}, fmt.Errorf("major %s: baseImage %q is not pinned by digest", entry.Major, entry.BaseImage)
		}
		if entry.Image != "" && !digestRef.MatchString(entry.Image) {
			return PostgresCatalog{}, fmt.Errorf("major %s: image %q is not pinned by digest", entry.Major, entry.Image)
		}
		if entry.DocumentDB && catalog.DocumentDBVersion == "" {
			return PostgresCatalog{}, fmt.Errorf("major %s claims DocumentDB support but documentDBVersion is not pinned", entry.Major)
		}
	}
	return catalog, nil
}

// PostgresMajors returns the supported majors in catalogue order.
func PostgresMajors() []string {
	majors := make([]string, 0, len(postgresCatalog.Majors))
	for _, entry := range postgresCatalog.Majors {
		majors = append(majors, entry.Major)
	}
	return majors
}

// PostgresCatalogEntries returns every catalogue entry in order.
func PostgresCatalogEntries() []PostgresMajorEntry {
	entries := make([]PostgresMajorEntry, len(postgresCatalog.Majors))
	copy(entries, postgresCatalog.Majors)
	return entries
}

// LookupPostgresMajor finds one catalogue entry. The second result is false
// for any major the platform does not support.
func LookupPostgresMajor(major string) (PostgresMajorEntry, bool) {
	wanted := strings.TrimSpace(major)
	for _, entry := range postgresCatalog.Majors {
		if entry.Major == wanted {
			return entry, true
		}
	}
	return PostgresMajorEntry{}, false
}

// DocumentDBSupported reports whether the image for a major carries
// DocumentDB. An unsupported major reports false.
func DocumentDBSupported(major string) bool {
	entry, ok := LookupPostgresMajor(major)
	return ok && entry.DocumentDB
}

// DocumentDBVersion is the pinned upstream DocumentDB release.
func DocumentDBVersion() string { return postgresCatalog.DocumentDBVersion }

// PostgresImage returns the digest-pinned image for a major. It errors — never
// falls back to a tag or to a neighbouring major — when the major is not in the
// catalogue or has not been published.
func PostgresImage(major string) (string, error) {
	entry, ok := LookupPostgresMajor(major)
	if !ok {
		return "", fmt.Errorf("postgres major %q is not supported (supported: %s)", major, strings.Join(PostgresMajors(), ", "))
	}
	if entry.Image == "" {
		return "", fmt.Errorf("postgres major %s has no published image; run the postgres-image-publish workflow and record the digest in postgres_catalog.yaml", entry.Major)
	}
	return entry.Image, nil
}

// SupportedPostgresMajorsMessage renders the supported set for error messages.
func SupportedPostgresMajorsMessage() string {
	return strings.Join(PostgresMajors(), ", ")
}

// DocumentDBMajors returns the majors whose image carries DocumentDB.
func DocumentDBMajors() []string {
	majors := make([]string, 0, len(postgresCatalog.Majors))
	for _, entry := range postgresCatalog.Majors {
		if entry.DocumentDB {
			majors = append(majors, entry.Major)
		}
	}
	return majors
}

// DocumentDBMajorsMessage renders the DocumentDB-capable majors for error
// messages.
func DocumentDBMajorsMessage() string {
	return strings.Join(DocumentDBMajors(), ", ")
}

// DockerPostgresImage returns the image the Docker deployment path runs for a
// major. Docker mode uses the official upstream image rather than the CNPG one
// — the CNPG images carry no entrypoint and only start under the operator —
// but it is still driven by the catalogue, so the two paths support exactly
// the same set of majors.
func DockerPostgresImage(major string) (string, error) {
	entry, ok := LookupPostgresMajor(major)
	if !ok {
		return "", fmt.Errorf("postgres version %q is not supported (supported: %s)", major, SupportedPostgresMajorsMessage())
	}
	return "postgres:" + entry.Major, nil
}
