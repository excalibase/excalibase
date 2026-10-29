package storage

import (
	"context"

	"github.com/excalibase/provisioning-poc/internal/domain"
)

// ProjectAuthHookStore persists each project's end-user auth hooks (EXC-662).
// The signing secret is never here: the store records only that the vault
// holds one, and a hook can be switched on only while it does.
type ProjectAuthHookStore interface {
	// ListAuthHooks returns the stored rows only, in no promised order.
	ListAuthHooks(ctx context.Context, projectID string) ([]domain.ProjectAuthHook, error)
	// PutAuthHook upserts one hook. Switching it on without a recorded
	// secret writes nothing and returns domain.ErrAuthHookNeedsSecret.
	PutAuthHook(ctx context.Context, projectID string, hook domain.ProjectAuthHook) error
	// AuthHookSecretSet reports whether the vault holds the signing secret.
	AuthHookSecretSet(ctx context.Context, projectID string) (bool, error)
	// MarkAuthHookSecretSet records that the vault now holds the secret.
	MarkAuthHookSecretSet(ctx context.Context, projectID string) error
	// LockAuthHookSecret serializes secret changes per project; the caller
	// holds it across the vault call and must call the release func.
	LockAuthHookSecret(ctx context.Context, projectID string) (func(), error)
	// ClearAuthHookSecret switches every hook of the project off and forgets
	// the secret, in one transaction.
	ClearAuthHookSecret(ctx context.Context, projectID string) error
}
