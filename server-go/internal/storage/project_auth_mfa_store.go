package storage

import "context"

// ProjectAuthMFAStore writes the project's end-user MFA switch (EXC-662).
// The switch is read with the sign-in policy (domain.ProjectAuthSignIn); the
// caller makes sure the vault holds the key before turning it on.
type ProjectAuthMFAStore interface {
	// MFAKeyFingerprint is the fingerprint of the key the project's factors are
	// sealed with, "" before MFA was first switched on.
	MFAKeyFingerprint(ctx context.Context, projectID string) (string, error)
	// EnableMFA turns the switch on, creating the settings row if needed, and
	// records keyFingerprint; another key's is refused with domain.ErrMFAKeyLost.
	EnableMFA(ctx context.Context, projectID, keyFingerprint string) error
	// DisableMFA turns the switch off; a project with no settings row is already off.
	DisableMFA(ctx context.Context, projectID string) error
}
