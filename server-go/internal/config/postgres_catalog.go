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
	// DocumentDBRef is the upstream git tag the DocumentDB extension is
	// compiled from, e.g. "v0.117-0". We build it rather than take upstream's
	// packages, so this is our choice to move, not theirs.
	DocumentDBRef string `json:"documentDBRef,omitempty"`
	// DocumentDBGatewayImage is the digest-pinned gateway image the sidecar
	// injector puts in a DocumentDB project's pod, and
	// DocumentDBGatewayImageTag the tag it was resolved from — kept so the
	// release the digest stands for can be read without a registry lookup.
	DocumentDBGatewayImage    string               `json:"documentDBGatewayImage,omitempty"`
	DocumentDBGatewayImageTag string               `json:"documentDBGatewayImageTag,omitempty"`
	Majors                    []PostgresMajorEntry `json:"majors"`
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
		if entry.DocumentDB && catalog.DocumentDBRef == "" {
			return PostgresCatalog{}, fmt.Errorf("major %s claims DocumentDB support but documentDBRef is not pinned", entry.Major)
		}
		// A DocumentDB project is only usable through the gateway, so a
		// catalogue that offers the extension without pinning the gateway
		// offers half a feature. Refuse at parse time rather than at the
		// provision that discovers it.
		if entry.DocumentDB && !digestRef.MatchString(catalog.DocumentDBGatewayImage) {
			return PostgresCatalog{}, fmt.Errorf("major %s claims DocumentDB support but documentDBGatewayImage %q is not pinned by digest",
				entry.Major, catalog.DocumentDBGatewayImage)
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

// DocumentDBRef is the pinned upstream DocumentDB tag the images compile.
func DocumentDBRef() string { return postgresCatalog.DocumentDBRef }

// DocumentDBGatewayImage is the digest-pinned gateway image a DocumentDB
// project's cluster tells the sidecar injector to run.
func DocumentDBGatewayImage() string { return postgresCatalog.DocumentDBGatewayImage }

// DocumentDBGatewayImageTag is the tag that digest was resolved from. It is
// documentation, never used to pull: the digest is what runs.
func DocumentDBGatewayImageTag() string { return postgresCatalog.DocumentDBGatewayImageTag }

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
	return taggedImageReference(entry), nil
}

// taggedImageReference names the major as a tag beside the pinned digest.
// CNPG refuses a digest-only spec.imageName ("Can't use just the image sha as
// we can't detect upgrades"); the digest still decides what is pulled.
func taggedImageReference(entry PostgresMajorEntry) string {
	repository, digest, found := strings.Cut(entry.Image, "@")
	if !found {
		// Unreachable: parsePostgresCatalog refuses an unpinned image.
		return entry.Image
	}
	return repository + ":" + entry.Major + "@" + digest
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
