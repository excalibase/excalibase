package service

import (
	"context"
	"errors"
	"fmt"
	"log"
	"slices"
	"strings"

	"github.com/excalibase/provisioning-poc/internal/apphost"
	"github.com/excalibase/provisioning-poc/internal/k8s"
	"github.com/excalibase/provisioning-poc/internal/storage"
	"github.com/excalibase/provisioning-poc/internal/vaultclient"
	"github.com/excalibase/provisioning-poc/pkg/vault"
)

const (
	registryUsernameKey = "username"
	registryPasswordKey = "password"
)

// ErrInvalidRegistryCredential marks input refused before anything is stored.
var ErrInvalidRegistryCredential = errors.New("invalid registry credential")

var errRegistryStoreUnreadable = errors.New("the credential store could not be read")

// RegistryCredentialService keeps a project's private-registry credentials in
// the project's vault. It is write-only: nothing it returns carries a username
// or a password, only which registries have one.
type RegistryCredentialService struct {
	vault     vaultclient.VaultClient
	instances storage.InstanceStore
	kube      k8s.KubeClient
}

func NewRegistryCredentialService(vault vaultclient.VaultClient, instances storage.InstanceStore, kube k8s.KubeClient) *RegistryCredentialService {
	return &RegistryCredentialService{vault: vault, instances: instances, kube: kube}
}

// Set stores the credential, replacing any the registry had, and returns the
// registry name it is filed under.
func (s *RegistryCredentialService) Set(projectID, registry string, cred apphost.RegistryCredential) (string, error) {
	normalized, err := apphost.NormalizeRegistry(registry)
	if err != nil {
		return "", fmt.Errorf("%w: %w", ErrInvalidRegistryCredential, err)
	}
	if err := cred.Validate(); err != nil {
		return "", fmt.Errorf("%w: %w", ErrInvalidRegistryCredential, err)
	}
	record := map[string]string{registryUsernameKey: cred.Username, registryPasswordKey: cred.Password}
	if err := s.vault.Put(apphost.RegistryCredentialPath(projectID, normalized), record); err != nil {
		log.Printf("registry credential: store for project %s: %v", projectID, err)
		return "", errors.New("the credential could not be stored")
	}
	return normalized, nil
}

// List names the registries the project holds a credential for, sorted.
func (s *RegistryCredentialService) List(projectID string) ([]string, error) {
	prefix := apphost.RegistryCredentialPrefix(projectID)
	paths, err := s.vault.List(prefix)
	if err != nil {
		log.Printf("registry credential: list for project %s: %v", projectID, err)
		return nil, errRegistryStoreUnreadable
	}
	registries := make([]string, 0, len(paths))
	for _, path := range paths {
		if registry, ok := strings.CutPrefix(path, prefix); ok && registry != "" && !strings.Contains(registry, "/") {
			registries = append(registries, registry)
		}
	}
	slices.Sort(registries)
	return registries, nil
}

// Remove forgets the credential and deletes every pull secret rendered from
// it, so the platform stops pulling with a credential the project took back.
// The vault goes first: a deploy that read the credential before then either
// wrote its secret before the cluster delete, or finds the credential gone
// when it checks again. A retry after a failed cluster delete still deletes by
// label, since removing an absent vault entry succeeds.
func (s *RegistryCredentialService) Remove(ctx context.Context, projectID, registry string) error {
	normalized, err := apphost.NormalizeRegistry(registry)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidRegistryCredential, err)
	}
	if err := s.vault.Delete(apphost.RegistryCredentialPath(projectID, normalized)); err != nil {
		log.Printf("registry credential: delete for project %s: %v", projectID, err)
		return errors.New("the credential could not be removed")
	}
	inst, err := s.instances.FindByProjectID(projectID)
	if err != nil {
		return fmt.Errorf("look up project namespace: %w", err)
	}
	if inst == nil || inst.Namespace == "" {
		return nil
	}
	if err := s.kube.DeleteRegistryPullSecrets(ctx, inst.Namespace, normalized); err != nil {
		return fmt.Errorf("remove the credential from the cluster: %w", err)
	}
	return nil
}

// Lookup returns the stored credential, nil when the project has none for the
// registry, and an error when the store cannot say which.
func (s *RegistryCredentialService) Lookup(projectID, registry string) (*apphost.RegistryCredential, error) {
	record, err := s.vault.Get(apphost.RegistryCredentialPath(projectID, registry))
	if errors.Is(err, vault.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		log.Printf("registry credential: read for project %s: %v", projectID, err)
		return nil, errRegistryStoreUnreadable
	}
	cred := apphost.RegistryCredential{Username: record[registryUsernameKey], Password: record[registryPasswordKey]}
	if err := cred.Validate(); err != nil {
		return nil, fmt.Errorf("the stored credential is damaged: %w", err)
	}
	return &cred, nil
}
