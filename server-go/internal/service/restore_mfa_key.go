package service

import (
	"context"
	"errors"
	"fmt"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/provisioner"
	"github.com/excalibase/provisioning-poc/pkg/vault"
)

var (
	errInvalidSourceMFAKey = errors.New("the source's MFA key is not a valid key")
	errCopyHoldsOtherKey   = errors.New("the copy already holds a different MFA key")
)

// carryMFAKey gives the copy the source's MFA key: the copied tenant database
// holds factors sealed with it. The key is carried even with MFA off, so
// switching it on later keeps those factors readable. Only a source with no
// key (or a platform with no vault) gives a copy with MFA off; any other
// failure fails the restore so its compensations run.
func (s *ProvisioningService) carryMFAKey(ctx context.Context, pc *provisioner.ProvisionContext, sourceID, targetID string) error {
	if s.vault == nil {
		return s.mfaOffOnCopy(ctx, targetID)
	}
	source, err := s.vault.Get(domain.MFAKeyPath(sourceID))
	if errors.Is(err, vault.ErrNotFound) {
		return s.mfaOffOnCopy(ctx, targetID)
	}
	if err != nil {
		return fmt.Errorf("read the source's MFA key: %w", err)
	}
	key := source[domain.MFAKeyField]
	if !domain.ValidMFAKey(key) {
		return errInvalidSourceMFAKey
	}
	return s.putCopyMFAKey(pc, targetID, key)
}

func (s *ProvisioningService) mfaOffOnCopy(ctx context.Context, targetID string) error {
	if err := s.restoredSettings.DisableMFA(ctx, targetID); err != nil {
		return fmt.Errorf("switch MFA off on the copy: %w", err)
	}
	return nil
}

// putCopyMFAKey never replaces a key at the copy's path; an identical one is
// already carried. A key it wrote is removed if the restore fails later.
func (s *ProvisioningService) putCopyMFAKey(pc *provisioner.ProvisionContext, targetID, key string) error {
	path := domain.MFAKeyPath(targetID)
	created, err := s.vault.PutIfAbsent(path, map[string]string{domain.MFAKeyField: key})
	if err != nil {
		return fmt.Errorf("write the copy's MFA key: %w", err)
	}
	if created {
		if pc != nil {
			pc.RegisterCleanup("delete vault "+path, func(context.Context) error { return s.vault.Delete(path) })
		}
		return nil
	}
	existing, err := s.vault.Get(path)
	if err != nil {
		return fmt.Errorf("read the copy's MFA key: %w", err)
	}
	if existing[domain.MFAKeyField] != key {
		return errCopyHoldsOtherKey
	}
	return nil
}
