package service

import (
	"context"
	"errors"
	"fmt"
	"log"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/pkg/vault"
)

var (
	errNoVaultForMFAKey  = errors.New("no vault is wired")
	errNoSourceMFAKey    = errors.New("the source holds no valid MFA key")
	errCopyHoldsOtherKey = errors.New("the copy already holds a different MFA key")
)

// carryMFAKey gives the copy the source's MFA key: the copied tenant database
// holds factors sealed with it. The key is carried even with MFA off, so
// switching it on later keeps those factors readable. Anything short of that
// leaves the copy's MFA off; only a failure to switch it off fails the restore.
func (s *ProvisioningService) carryMFAKey(ctx context.Context, sourceID, targetID string) error {
	err := s.copyMFAKey(sourceID, targetID)
	if err == nil {
		return nil
	}
	log.Printf("WARN: restored copy %s keeps MFA off: the key of %s was not carried: %v", targetID, sourceID, err)
	if err := s.restoredSettings.DisableMFA(ctx, targetID); err != nil {
		return fmt.Errorf("switch MFA off on the copy: %w", err)
	}
	return nil
}

func (s *ProvisioningService) copyMFAKey(sourceID, targetID string) error {
	if s.vault == nil {
		return errNoVaultForMFAKey
	}
	source, err := s.vault.Get(domain.MFAKeyPath(sourceID))
	if err != nil {
		return fmt.Errorf("read the source key: %w", err)
	}
	key := source[domain.MFAKeyField]
	if !domain.ValidMFAKey(key) {
		return errNoSourceMFAKey
	}
	// Never replace a key at the copy's path; an identical one is already carried.
	existing, err := s.vault.Get(domain.MFAKeyPath(targetID))
	switch {
	case err == nil && existing[domain.MFAKeyField] == key:
		return nil
	case err == nil:
		return errCopyHoldsOtherKey
	case !errors.Is(err, vault.ErrNotFound):
		return fmt.Errorf("read the copy's key: %w", err)
	}
	if err := s.vault.Put(domain.MFAKeyPath(targetID), map[string]string{domain.MFAKeyField: key}); err != nil {
		return fmt.Errorf("write the copy's key: %w", err)
	}
	return nil
}
