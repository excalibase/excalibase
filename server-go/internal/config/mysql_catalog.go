package config

import (
	_ "embed"
	"fmt"
	"strconv"
	"strings"

	"k8s.io/apimachinery/pkg/api/resource"
	"sigs.k8s.io/yaml"
)

//go:embed mysql_catalog.yaml
var mysqlCatalogYAML []byte

// MySQLSeries is the one MySQL series projects run (ADR 0041).
const MySQLSeries = "8.4"

// MySQLVersionEntry is one MySQL version and the image that serves it.
type MySQLVersionEntry struct {
	Version string `json:"version"`
	// Image is tagged with Version and pinned by digest.
	Image string `json:"image"`
}

// MySQLCatalog is the catalogue as it appears on disk, newest first.
type MySQLCatalog struct {
	Versions []MySQLVersionEntry `json:"versions"`
}

var mysqlCatalog MySQLCatalog

func init() {
	catalog, err := parseMySQLCatalog(mysqlCatalogYAML)
	if err != nil {
		panic(fmt.Sprintf("mysql catalog: %v", err))
	}
	mysqlCatalog = catalog
}

func parseMySQLCatalog(raw []byte) (MySQLCatalog, error) {
	var catalog MySQLCatalog
	if err := yaml.UnmarshalStrict(raw, &catalog); err != nil {
		return MySQLCatalog{}, fmt.Errorf("parse: %w", err)
	}
	if len(catalog.Versions) == 0 {
		return MySQLCatalog{}, fmt.Errorf("catalogue lists no version")
	}
	var previous []int
	for _, entry := range catalog.Versions {
		if err := validateMySQLEntry(entry); err != nil {
			return MySQLCatalog{}, err
		}
		parts, _ := mysqlVersionParts(entry.Version)
		if previous != nil && compareVersionParts(parts, previous) >= 0 {
			return MySQLCatalog{}, fmt.Errorf("version %s is listed after %v: the catalogue is newest first, without repeats", entry.Version, previous)
		}
		previous = parts
	}
	return catalog, nil
}

func validateMySQLEntry(entry MySQLVersionEntry) error {
	if _, err := mysqlVersionParts(entry.Version); err != nil {
		return err
	}
	if !strings.HasPrefix(entry.Version, MySQLSeries+".") {
		return fmt.Errorf("version %s is not MySQL %s", entry.Version, MySQLSeries)
	}
	if !digestRef.MatchString(entry.Image) {
		return fmt.Errorf("version %s: image %q is not pinned by digest", entry.Version, entry.Image)
	}
	name, _, _ := strings.Cut(entry.Image, "@")
	if !strings.HasSuffix(name, ":"+entry.Version) {
		return fmt.Errorf("version %s: image %q is not tagged with its version", entry.Version, entry.Image)
	}
	return nil
}

// mysqlVersionParts reads "8.4.10" as [8 4 10].
func mysqlVersionParts(version string) ([]int, error) {
	fields := strings.Split(version, ".")
	if len(fields) != 3 {
		return nil, fmt.Errorf("version %q is not major.minor.patch", version)
	}
	parts := make([]int, len(fields))
	for i, field := range fields {
		n, err := strconv.Atoi(field)
		if err != nil || n < 0 {
			return nil, fmt.Errorf("version %q is not major.minor.patch", version)
		}
		parts[i] = n
	}
	return parts, nil
}

func compareVersionParts(a, b []int) int {
	for i := range a {
		if a[i] != b[i] {
			if a[i] < b[i] {
				return -1
			}
			return 1
		}
	}
	return 0
}

// MySQLVersions lists the supported versions, newest first.
func MySQLVersions() []string {
	versions := make([]string, 0, len(mysqlCatalog.Versions))
	for _, entry := range mysqlCatalog.Versions {
		versions = append(versions, entry.Version)
	}
	return versions
}

// MySQLCatalogEntries returns every catalogue entry, newest first.
func MySQLCatalogEntries() []MySQLVersionEntry {
	entries := make([]MySQLVersionEntry, len(mysqlCatalog.Versions))
	copy(entries, mysqlCatalog.Versions)
	return entries
}

// DefaultMySQLVersion is what a project that names no version gets: the newest.
func DefaultMySQLVersion() string { return mysqlCatalog.Versions[0].Version }

// MySQLImage is the pinned image for a version; any version the catalogue
// does not list is refused, never substituted.
func MySQLImage(version string) (string, error) {
	wanted := strings.TrimSpace(version)
	for _, entry := range mysqlCatalog.Versions {
		if entry.Version == wanted {
			return entry.Image, nil
		}
	}
	return "", fmt.Errorf("mysql version %q is not supported (supported: %s)", version, strings.Join(MySQLVersions(), ", "))
}

// MySQLUpgradeAllowed accepts only a later version of the same series that
// the catalogue lists.
func MySQLUpgradeAllowed(from, to string) error {
	if _, err := MySQLImage(to); err != nil {
		return err
	}
	current, err := mysqlVersionParts(from)
	if err != nil {
		return err
	}
	next, _ := mysqlVersionParts(to)
	if current[0] != next[0] || current[1] != next[1] {
		return fmt.Errorf("moving from mysql %s to %s changes the series; only minor upgrades within %d.%d are supported", from, to, current[0], current[1])
	}
	if compareVersionParts(next, current) <= 0 {
		return fmt.Errorf("mysql %s is not newer than %s; a version is never downgraded", to, from)
	}
	return nil
}

// MySQLMinMemory is the least a MySQL instance runs on. At 512Mi mysqld sat
// at its limit idle and was OOM-killed restarting onto a new version (EXC-583).
const MySQLMinMemory = "1Gi"

// MySQLPlan is the plan a MySQL project runs on: the tier, with its memory
// raised to MySQLMinMemory. Capacity checks and the cluster both use it.
func MySQLPlan(plan TierConfig) (TierConfig, error) {
	memory, err := resource.ParseQuantity(plan.Memory)
	if err != nil {
		return TierConfig{}, fmt.Errorf("plan memory %q: %w", plan.Memory, err)
	}
	if memory.Cmp(resource.MustParse(MySQLMinMemory)) < 0 {
		plan.Memory = MySQLMinMemory
	}
	return plan, nil
}
