package sitebuilds

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"github.com/excalibase/provisioning-poc/internal/vaultclient"
	"github.com/excalibase/provisioning-poc/pkg/vault"
)

// SecretReader reads the secrets a project keeps (Studio → Edge Functions →
// Secrets). A build sees only the ones marked build-visible.
type SecretReader interface {
	Secrets(ctx context.Context, projectID string) (map[string]string, error)
}

// VaultSecrets reads the project's secrets where the edge-function store keeps them.
type VaultSecrets struct{ vault vaultclient.VaultClient }

func NewVaultSecrets(client vaultclient.VaultClient) *VaultSecrets {
	return &VaultSecrets{vault: client}
}

// Secrets answers none for a project that never set any; any other vault failure is an error, never "none".
func (v *VaultSecrets) Secrets(_ context.Context, projectID string) (map[string]string, error) {
	if v.vault == nil {
		return nil, errors.New("no vault to read project secrets from")
	}
	values, err := v.vault.Get(fmt.Sprintf("projects/%s/edgefn/secrets", projectID))
	if errors.Is(err, vault.ErrNotFound) {
		return map[string]string{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read the project's secrets: %w", err)
	}
	if values == nil {
		return map[string]string{}, nil
	}
	return values, nil
}

// SecretVisibility is one project secret and whether builds see it.
type SecretVisibility struct {
	Name         string `json:"name"`
	BuildVisible bool   `json:"buildVisible"`
}

func sortedNames(values map[string]string) []string {
	names := make([]string, 0, len(values))
	for name := range values {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
