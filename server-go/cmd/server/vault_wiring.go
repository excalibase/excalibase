package main

import (
	"github.com/excalibase/provisioning-poc/internal/config"
	pgstore "github.com/excalibase/provisioning-poc/internal/storage/postgres"
	"github.com/excalibase/provisioning-poc/pkg/vault"
)

// localVaultNeedsAutoReady reports whether the in-process vault must init and
// unseal itself at boot. Cloud deployments have a bootstrap Job that does this
// step; selfhosted and docker deployments have nobody else to do it.
func localVaultNeedsAutoReady(cfg config.AppConfig) bool {
	return !cfg.IsCloud()
}

// newLocalVault opens the in-process vault on the given store and, when
// autoReady is set, brings it to an unsealed state via vault.EnsureReady.
func newLocalVault(store vault.VaultStore, autoReady bool, keyFilePath, envKey string) (*vault.Vault, error) {
	localVault, err := vault.NewWithStore(store)
	if err != nil {
		return nil, err
	}
	if !autoReady {
		return localVault, nil
	}
	if err := vault.EnsureReady(localVault, keyFilePath, envKey); err != nil {
		return nil, err
	}
	return localVault, nil
}

// openVaultOnPlatformDB opens the vault on the platform store's connection
// without any auto-ready step; CLI subcommands unseal interactively.
func openVaultOnPlatformDB(platformStore *pgstore.Store) (*vault.Vault, error) {
	return vault.NewWithStore(vault.NewPostgresStore(platformStore.DB()))
}
