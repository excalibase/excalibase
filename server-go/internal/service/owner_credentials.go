package service

import (
	"errors"
	"fmt"
)

// ErrOwnerCredentialUnavailable is returned when a project's owner password
// cannot be read from, or kept in, the vault. The vault is its only home.
var ErrOwnerCredentialUnavailable = errors.New("the project's owner credential is not available from the vault")

// OwnerCredentials answers a project's owner password.
type OwnerCredentials interface {
	OwnerPassword(projectID string) (string, error)
}

// OwnerPassword reads the owner password filed for the project in the vault.
func (s *ProvisioningService) OwnerPassword(projectID string) (string, error) {
	if s.vault == nil || s.vault.Sealed() {
		return "", fmt.Errorf("%w: the vault is not available", ErrOwnerCredentialUnavailable)
	}
	record, err := s.vault.Get(vaultCredentialPath(projectID, roleAdmin))
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrOwnerCredentialUnavailable, err)
	}
	if record["password"] == "" {
		return "", fmt.Errorf("%w: no password is filed for project %s", ErrOwnerCredentialUnavailable, projectID)
	}
	return record["password"], nil
}
