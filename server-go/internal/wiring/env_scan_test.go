package wiring

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// A setting read straight from the environment somewhere in the tree is a
// setting the boot-time wiring check never sees: it cannot know the feature
// exists, let alone that it is half-configured. So environment reads belong
// in internal/config, and every remaining one is listed here on purpose.
//
// The allow-list is per file. Adding an entry is a deliberate act; the
// reason each is here is written next to it.
var envReadAllowList = map[string]string{
	// The configuration package is where the environment is read. The whole
	// package is allowed, not one file: settings are grouped by subject as
	// they grow, and a new file there is the intended shape, not an escape.
	"internal/config/": "the one place settings are loaded",

	// Local-development address overrides for a tenant connection. They are
	// pure tunables — nothing selects a provider or turns a subsystem on —
	// and each caller reads the same three through projectdb.Overrides.
	"internal/projectdb/opener.go":            "SCHEMA_DB_* development overrides",
	"internal/handler/schema.go":              "SCHEMA_DB_* development overrides",
	"internal/service/migration.go":           "SCHEMA_DB_* development overrides",
	"internal/service/database_probe.go":      "SCHEMA_DB_* development overrides",
	"internal/service/credential_rotation.go": "SCHEMA_DB_* development overrides, read through the same projectdb.Overrides as the probe",

	// Cluster and daemon discovery, which is the runtime's own environment
	// rather than platform configuration.
	"internal/k8s/client.go":                "KUBECONFIG discovery",
	"internal/k8s/deno_runtime.go":          "POD_NAMESPACE, the pod's own identity",
	"internal/provisioner/docker_client.go": "DOCKER_HOST discovery",

	// Operator tunables and one-off CLI subcommands in the entrypoint. None
	// of them selects a provider or enables a subsystem; the switches that
	// do were moved into internal/config.
	"cmd/server/main.go": "tunables (STORAGE_REAP_GRACE, DELETION_WAIT_TIMEOUT, REALTIME_PUBLICATION_NAME, BACKUP_S3_PATH_STYLE), the unseal material and the kms-encrypt-unseal CLI",

	// Seal material read by the vault at the moment it unseals, and the KMS
	// client's own SDK endpoint override. Both are secrets or SDK plumbing
	// rather than feature switches, and pkg/** is a standalone library.
	"pkg/vault/vault.go":     "VAULT_UNSEAL_KEY at unseal time",
	"pkg/kmsseal/kmsseal.go": "KMS endpoint override and the wrapped unseal material",
}

var envReadPattern = regexp.MustCompile(`os\.(Getenv|LookupEnv)\(`)

func TestEnvironmentIsReadInConfig(t *testing.T) {
	root := filepath.Join("..", "..")
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if !envReadPattern.Match(body) {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		allowed := false
		for entry := range envReadAllowList {
			if rel == entry || (strings.HasSuffix(entry, "/") && strings.HasPrefix(rel, entry)) {
				allowed = true
				break
			}
		}
		if !allowed {
			t.Errorf("%s reads the environment directly; move the setting into internal/config "+
				"so the startup wiring check can see it, or add it to the allow-list with a reason", rel)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
}

// An allow-list entry for a file that no longer reads the environment is
// stale and would hide the next one that appears there.
func TestEnvReadAllowListHasNoStaleEntries(t *testing.T) {
	root := filepath.Join("..", "..")
	for rel := range envReadAllowList {
		if strings.HasSuffix(rel, "/") {
			if !dirReadsEnvironment(t, filepath.Join(root, filepath.FromSlash(rel))) {
				t.Errorf("%s no longer reads the environment; drop its allow-list entry", rel)
			}
			continue
		}
		body, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
		if err != nil {
			t.Errorf("%s is allow-listed but missing: %v", rel, err)
			continue
		}
		if !envReadPattern.Match(body) {
			t.Errorf("%s no longer reads the environment; drop its allow-list entry", rel)
		}
	}
}

// dirReadsEnvironment reports whether any Go file directly in dir reads the
// environment, so an allow-listed package is still earning its entry.
func dirReadsEnvironment(t *testing.T, dir string) bool {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Errorf("%s is allow-listed but missing: %v", dir, err)
		return true
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") {
			continue
		}
		body, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		if envReadPattern.Match(body) {
			return true
		}
	}
	return false
}
