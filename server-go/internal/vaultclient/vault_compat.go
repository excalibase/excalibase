package vaultclient

import "github.com/excalibase/provisioning-poc/pkg/vault"

// Compile-time check: *vault.Vault satisfies VaultClient.
var _ VaultClient = (*vault.Vault)(nil)
