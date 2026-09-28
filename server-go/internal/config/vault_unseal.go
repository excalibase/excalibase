package config

import (
	"errors"
	"fmt"
	"os"
)

// Vault unseal providers (EXC-485). awskms keeps only a KMS ciphertext of the
// unseal key; plaintext keeps the key itself and is for development.
const (
	UnsealProviderAWSKMS    = "awskms"
	UnsealProviderPlaintext = "plaintext"
)

// VaultUnseal is how the in-process vault gets its unseal key at boot.
type VaultUnseal struct {
	// Provider is awskms, plaintext, or "" (the plaintext default of the
	// docker provisioner, which keeps its key in a file).
	Provider string
	// KMSKeyID is the key new unseal shares are encrypted under.
	KMSKeyID string
	// Ciphertext is the wrapped unseal key; empty before the first init.
	Ciphertext string
}

// UsesKMS reports whether the unseal key is kept under KMS.
func (u VaultUnseal) UsesKMS() bool { return u.Provider == UnsealProviderAWSKMS }

// LoadVaultUnseal reads the unseal settings from the environment.
func LoadVaultUnseal() (VaultUnseal, error) { return ParseVaultUnseal(os.Getenv) }

// ParseVaultUnseal validates the unseal settings. There is no fallback between
// providers: material for one provider set alongside another is refused, so a
// KMS install can never quietly boot from a plaintext key.
func ParseVaultUnseal(get func(string) string) (VaultUnseal, error) {
	u := VaultUnseal{
		Provider:   get("VAULT_UNSEAL_PROVIDER"),
		KMSKeyID:   get("VAULT_KMS_KEY_ID"),
		Ciphertext: get("VAULT_UNSEAL_KEY_CIPHERTEXT"),
	}
	plaintextKey := get("VAULT_UNSEAL_KEY") != ""
	switch u.Provider {
	case UnsealProviderAWSKMS:
		if u.KMSKeyID == "" {
			return VaultUnseal{}, errors.New("VAULT_UNSEAL_PROVIDER=awskms needs VAULT_KMS_KEY_ID (the KMS key ARN)")
		}
		if plaintextKey {
			return VaultUnseal{}, errors.New("VAULT_UNSEAL_PROVIDER=awskms refuses a plaintext VAULT_UNSEAL_KEY: remove it")
		}
	case UnsealProviderPlaintext, "":
		if u.Ciphertext != "" {
			return VaultUnseal{}, errors.New("VAULT_UNSEAL_KEY_CIPHERTEXT is set but VAULT_UNSEAL_PROVIDER is not awskms: set VAULT_UNSEAL_PROVIDER=awskms")
		}
		if u.KMSKeyID != "" {
			return VaultUnseal{}, errors.New("VAULT_KMS_KEY_ID is set but VAULT_UNSEAL_PROVIDER is not awskms: set VAULT_UNSEAL_PROVIDER=awskms")
		}
	default:
		return VaultUnseal{}, fmt.Errorf("VAULT_UNSEAL_PROVIDER=%q is not supported: use awskms or plaintext", u.Provider)
	}
	return u, nil
}

// CheckDeployment refuses awskms where it would configure nothing: a remote
// vault unseals itself, and the docker provisioner keeps its key in a file.
func (u VaultUnseal) CheckDeployment(provisionerMode, vaultURL string) error {
	if !u.UsesKMS() {
		return nil
	}
	if vaultURL != "" {
		return errors.New("VAULT_UNSEAL_PROVIDER=awskms applies to the in-process vault, but VAULT_URL points at a remote one")
	}
	if provisionerMode == "docker" {
		return errors.New("VAULT_UNSEAL_PROVIDER=awskms is not supported by the docker provisioner")
	}
	return nil
}
