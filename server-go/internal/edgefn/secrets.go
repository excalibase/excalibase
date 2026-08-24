package edgefn

import (
	"fmt"
	"regexp"
	"sort"

	"github.com/excalibase/provisioning-poc/internal/vaultclient"
)

// MaxSecretCount caps secrets per project. Keeps bundle size bounded and
// discourages misuse as general-purpose key-value storage.
const MaxSecretCount = 100

// MaxSecretValueLen caps the length of a single secret value.
const MaxSecretValueLen = 4096

// secretKeyPattern — env var naming: letters, digits, underscore, no leading digit.
var secretKeyPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,63}$`)

// reservedSecretKeys are injected automatically by the runtime and cannot be overridden.
var reservedSecretKeys = map[string]bool{
	"EXCALIBASE_URL":         true,
	"EXCALIBASE_PROJECT_ID":  true,
	"EXCALIBASE_ANON_KEY":    true,
	"EXCALIBASE_SERVICE_KEY": true,
	"EXCALIBASE_DB_URL":      true,
}

// ValidateSecretKey returns nil if the key is a valid env var identifier
// and not reserved by the platform.
func ValidateSecretKey(key string) error {
	if !secretKeyPattern.MatchString(key) {
		return fmt.Errorf("invalid secret key: must match [A-Za-z_][A-Za-z0-9_]{0,63}")
	}
	if reservedSecretKeys[key] {
		return fmt.Errorf("secret key %q is reserved and cannot be set", key)
	}
	return nil
}

// SecretsStore persists per-project function secrets in the vault.
// Vault path: projects/{projectId}/edgefn/secrets — project-scoped only,
// no org dimension (projectId is globally unique).
// Stored as a single map[string]string entry — all secrets for a project
// are loaded and reloaded together. Keeps the worker deploy step simple.
type SecretsStore struct {
	vault vaultclient.VaultClient
}

func NewSecretsStore(vault vaultclient.VaultClient) *SecretsStore {
	return &SecretsStore{vault: vault}
}

func (s *SecretsStore) vaultPath(projectID string) string {
	return fmt.Sprintf("projects/%s/edgefn/secrets", projectID)
}

// GetAll returns every secret for the project as a map. Missing/empty returns
// an empty map, not an error — this is the normal state for new projects.
func (s *SecretsStore) GetAll(projectID string) (map[string]string, error) {
	if s.vault == nil {
		return map[string]string{}, nil
	}
	data, err := s.vault.Get(s.vaultPath(projectID))
	if err != nil {
		// Vault returns "not found" for new projects — return empty.
		return map[string]string{}, nil
	}
	if data == nil {
		return map[string]string{}, nil
	}
	return data, nil
}

// ListKeys returns only the keys (not values) in deterministic order.
func (s *SecretsStore) ListKeys(projectID string) ([]string, error) {
	all, err := s.GetAll(projectID)
	if err != nil {
		return nil, err
	}
	keys := make([]string, 0, len(all))
	for k := range all {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys, nil
}

// Set stores (or updates) a single secret. Reads the full map, updates one key,
// writes it back. Not atomic across concurrent callers — acceptable for a low-
// traffic admin surface.
func (s *SecretsStore) Set(projectID, key, value string) error {
	if s.vault == nil {
		return fmt.Errorf("vault not configured")
	}
	if err := ValidateSecretKey(key); err != nil {
		return err
	}
	if len(value) == 0 {
		return fmt.Errorf("secret value must not be empty")
	}
	if len(value) > MaxSecretValueLen {
		return fmt.Errorf("secret value exceeds max length %d", MaxSecretValueLen)
	}
	all, err := s.GetAll(projectID)
	if err != nil {
		return err
	}
	if _, exists := all[key]; !exists && len(all) >= MaxSecretCount {
		return fmt.Errorf("project has reached the max of %d secrets", MaxSecretCount)
	}
	all[key] = value
	return s.vault.Put(s.vaultPath(projectID), all)
}

// Delete removes a single key from the project's secret map. Missing key is not
// an error (idempotent).
func (s *SecretsStore) Delete(projectID, key string) error {
	if s.vault == nil {
		return fmt.Errorf("vault not configured")
	}
	if err := ValidateSecretKey(key); err != nil {
		return err
	}
	all, err := s.GetAll(projectID)
	if err != nil {
		return err
	}
	if _, ok := all[key]; !ok {
		return nil
	}
	delete(all, key)
	return s.vault.Put(s.vaultPath(projectID), all)
}

// BuildEnvForDeploy merges user secrets with the platform's built-in env vars.
// Built-ins always win over user values — platform identity is not overridable.
func (s *SecretsStore) BuildEnvForDeploy(projectID string, builtins map[string]string) (map[string]string, error) {
	user, err := s.GetAll(projectID)
	if err != nil {
		return nil, err
	}
	merged := make(map[string]string, len(user)+len(builtins))
	for k, v := range user {
		merged[k] = v
	}
	for k, v := range builtins {
		merged[k] = v
	}
	return merged, nil
}
