package s3keys

import (
	"errors"
	"fmt"

	"github.com/excalibase/provisioning-poc/internal/vaultclient"
	"github.com/excalibase/provisioning-poc/pkg/vault"
)

const secretField = "secret"

// VaultSecrets keeps each secret at projects/{projectId}/storage-s3/{accessKeyId},
// under the project's prefix so the project's teardown removes it.
type VaultSecrets struct{ vault vaultclient.VaultClient }

func NewVaultSecrets(v vaultclient.VaultClient) *VaultSecrets { return &VaultSecrets{vault: v} }

func secretPath(projectID, accessKeyID string) string {
	return fmt.Sprintf("projects/%s/storage-s3/%s", projectID, accessKeyID)
}

func (s *VaultSecrets) Put(projectID, accessKeyID, secret string) error {
	return s.vault.Put(secretPath(projectID, accessKeyID), map[string]string{secretField: secret})
}

func (s *VaultSecrets) Get(projectID, accessKeyID string) (string, error) {
	data, err := s.vault.Get(secretPath(projectID, accessKeyID))
	if errors.Is(err, vault.ErrNotFound) {
		return "", ErrSecretMissing
	}
	if err != nil {
		return "", err
	}
	secret := data[secretField]
	if secret == "" {
		return "", ErrSecretMissing
	}
	return secret, nil
}

func (s *VaultSecrets) Delete(projectID, accessKeyID string) error {
	err := s.vault.Delete(secretPath(projectID, accessKeyID))
	if errors.Is(err, vault.ErrNotFound) {
		return nil
	}
	return err
}
