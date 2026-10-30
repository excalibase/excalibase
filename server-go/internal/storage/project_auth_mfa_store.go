package storage

import "context"

// ProjectAuthMFAStore writes the project's end-user MFA switch (EXC-662).
// The switch is read with the sign-in policy (domain.ProjectAuthSignIn).
type ProjectAuthMFAStore interface {
	// EnableMFA runs ensureKey and then turns the switch on, one caller per
	// project at a time, so two enables never both write a new key. An error
	// from ensureKey is returned and the switch stays as it was.
	EnableMFA(ctx context.Context, projectID string, ensureKey func() error) error
	// DisableMFA turns the switch off; a project with no settings row is already off.
	DisableMFA(ctx context.Context, projectID string) error
}
