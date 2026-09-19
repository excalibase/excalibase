package main

import (
	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/storage"
	pgstore "github.com/excalibase/provisioning-poc/internal/storage/postgres"
	"github.com/excalibase/provisioning-poc/pkg/vault"
)

// localVaultNeedsAutoReady reports whether the in-process vault must init and
// unseal itself at boot. On k8s the chart's bootstrap Job does this step in
// every deployment mode and keeps the unseal key in a Secret (STORAGE_PATH may
// be an emptyDir, so a key file there would not survive a restart). The docker
// provisioner has no Job, so the binary readies itself.
func localVaultNeedsAutoReady(cfg config.AppConfig) bool {
	return cfg.ProvisionerMode == "docker"
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

// buildParameterGroupStore opens the filesystem-backed parameter-group store.
// Its error must abort boot: the constructor returns a nil pointer on
// directory-creation failure and every method dereferences the receiver, so a
// discarded error leaves a store that panics on the first request.
func buildParameterGroupStore(cfg config.AppConfig) (*storage.FileSystemParameterGroupStore, error) {
	return storage.NewFileSystemParameterGroupStore(cfg.StoragePath)
}
