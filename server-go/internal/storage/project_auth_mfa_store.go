package storage

import "context"

// ProjectAuthMFAStore writes the project's end-user MFA switch (EXC-662).
// The switch is read with the sign-in policy (domain.ProjectAuthSignIn); the
// caller makes sure the vault holds the key before turning it on.
type ProjectAuthMFAStore interface {
	// EnableMFA turns the switch on, creating the settings row if needed.
	EnableMFA(ctx context.Context, projectID string) error
	// DisableMFA turns the switch off; a project with no settings row is already off.
	DisableMFA(ctx context.Context, projectID string) error
}
