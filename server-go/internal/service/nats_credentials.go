// Package service — minting and revoking per-project NATS bus credentials.
package service

import (
	"context"
	"fmt"

	"github.com/excalibase/provisioning-poc/internal/natsauth"
	"github.com/excalibase/provisioning-poc/internal/storage"
)

// NatsCredentialMinter issues the credential a project's CDC watcher uses
// to reach the bus. The plaintext is returned exactly once — it goes into
// the watcher's k8s Secret — and only its bcrypt hash is persisted.
//
// A nil minter is a no-op that yields an empty credential, which is how a
// platform with auth_callout disabled (local dev) keeps working.
type NatsCredentialMinter struct {
	store storage.NatsCredentialStore
}

func NewNatsCredentialMinter(store storage.NatsCredentialStore) *NatsCredentialMinter {
	if store == nil {
		return nil
	}
	return &NatsCredentialMinter{store: store}
}

// MintTenantWatcher rotates the project's watcher credential. Re-provisioning
// therefore invalidates whatever a previous watcher pod was holding.
func (m *NatsCredentialMinter) MintTenantWatcher(ctx context.Context, projectID string) (string, string, error) {
	if m == nil {
		return "", "", nil
	}
	principal := natsauth.TenantWatcherPrincipal(projectID)
	// Refuse ids that would widen the subject the watcher may publish on.
	if _, ok := natsauth.ProjectIDForPrincipal(principal); !ok {
		return "", "", fmt.Errorf("project id is not a valid NATS subject token")
	}

	password, err := natsauth.NewPassword()
	if err != nil {
		return "", "", fmt.Errorf("mint nats password: %w", err)
	}
	hash, err := natsauth.HashPassword(password)
	if err != nil {
		return "", "", fmt.Errorf("hash nats password: %w", err)
	}
	if err := m.store.UpsertNatsCredential(ctx, principal, projectID, hash); err != nil {
		return "", "", fmt.Errorf("store nats credential: %w", err)
	}
	return principal, password, nil
}

// RevokeProject removes every bus credential a project holds, so a deleted
// project's watcher can never reconnect.
func (m *NatsCredentialMinter) RevokeProject(ctx context.Context, projectID string) error {
	if m == nil {
		return nil
	}
	return m.store.DeleteNatsCredentialsForProject(ctx, projectID)
}
