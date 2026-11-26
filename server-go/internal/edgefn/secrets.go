package edgefn

import (
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync"

	"github.com/excalibase/provisioning-poc/internal/vaultclient"
	"github.com/excalibase/provisioning-poc/pkg/vault"
)

// ErrSecretsUnavailable marks a vault read or write that failed, as opposed to
// a request the store refused: callers answer it as a service problem, not as
// a bad request, and never show its cause to a customer.
var ErrSecretsUnavailable = errors.New("function secrets are unavailable")

// MaxSecretCount caps secrets per project. Keeps bundle size bounded and
// discourages misuse as general-purpose key-value storage.
const MaxSecretCount = 100

// MaxSecretValueLen caps the length of a single secret value.
const MaxSecretValueLen = 4096

// secretKeyPattern — env var naming: letters, digits, underscore, no leading digit.
var secretKeyPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,63}$`)

// reservedSecretKeys are injected automatically by the runtime and cannot be overridden.
var reservedSecretKeys = map[string]bool{
	"EXCALIBASE_URL":        true,
	"EXCALIBASE_PROJECT_ID": true,
	"EXCALIBASE_DB_URL":     true,
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
//
// Set and Delete read the whole map and write it back, so every change to a
// project's secrets runs under LockProjectSecrets: two at once would both
// start from the same map and the second write would undo the first (EXC-897).
type SecretsStore struct {
	vault vaultclient.VaultClient
}

// projectSecretLocks holds one lock per project, shared by everything in this
// process that changes the project's function secrets: the store, the vault
// route a platform admin writes through, and project deletion. It lives in
// this process; the control plane runs as a single replica.
var projectSecretLocks sync.Map // projectID -> *sync.Mutex

// LockProjectSecrets takes the project's secrets lock and returns the unlock.
func LockProjectSecrets(projectID string) func() {
	lock, _ := projectSecretLocks.LoadOrStore(projectID, &sync.Mutex{})
	mutex := lock.(*sync.Mutex)
	mutex.Lock()
	return mutex.Unlock
}

// FunctionSecretsProject names the project whose function secrets a vault path
// holds, and reports whether the path is exactly that.
func FunctionSecretsProject(path string) (string, bool) {
	rest, found := strings.CutPrefix(path, "projects/")
	projectID, tail, cut := strings.Cut(rest, "/")
	if !found || !cut || projectID == "" || tail != "edgefn/secrets" {
		return "", false
	}
	return projectID, true
}

func NewSecretsStore(vault vaultclient.VaultClient) *SecretsStore {
	return &SecretsStore{vault: vault}
}

func (s *SecretsStore) vaultPath(projectID string) string {
	return fmt.Sprintf("projects/%s/edgefn/secrets", projectID)
}

// GetAll returns every secret for the project as a map. A project that has
// none yet returns an empty map — the normal state for new projects. Any other
// vault failure is an error: reading it as "no secrets" would deploy a function
// without them, and let Set overwrite the project's other secrets (EXC-447).
func (s *SecretsStore) GetAll(projectID string) (map[string]string, error) {
	if s.vault == nil {
		return map[string]string{}, nil
	}
	data, err := s.vault.Get(s.vaultPath(projectID))
	if errors.Is(err, vault.ErrNotFound) {
		return map[string]string{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("%w: read: %w", ErrSecretsUnavailable, err)
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

// Set stores (or updates) a single secret. Reads the full map, updates one key
// and writes it back, all under the project's lock.
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
	defer LockProjectSecrets(projectID)()
	all, err := s.GetAll(projectID)
	if err != nil {
		return err
	}
	if _, exists := all[key]; !exists && len(all) >= MaxSecretCount {
		return fmt.Errorf("project has reached the max of %d secrets", MaxSecretCount)
	}
	all[key] = value
	return s.put(projectID, all)
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
	defer LockProjectSecrets(projectID)()
	all, err := s.GetAll(projectID)
	if err != nil {
		return err
	}
	if _, ok := all[key]; !ok {
		return nil
	}
	delete(all, key)
	return s.put(projectID, all)
}

func (s *SecretsStore) put(projectID string, all map[string]string) error {
	if err := s.vault.Put(s.vaultPath(projectID), all); err != nil {
		return fmt.Errorf("%w: write: %w", ErrSecretsUnavailable, err)
	}
	return nil
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
