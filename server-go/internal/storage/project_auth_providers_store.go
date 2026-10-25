package storage

import (
	"context"

	"github.com/excalibase/provisioning-poc/internal/domain"
)

// ProjectAuthProviderStore persists each project's end-user OAuth providers
// (EXC-662). The client secret is never here: ClientSecretSet records that the
// vault holds one.
type ProjectAuthProviderStore interface {
	// ListAuthProviders returns the stored rows only, in no promised order.
	ListAuthProviders(ctx context.Context, projectID string) ([]domain.ProjectAuthProvider, error)
	// PutAuthProvider upserts one provider's row.
	PutAuthProvider(ctx context.Context, projectID string, provider domain.ProjectAuthProvider) error
	// DeleteAuthProvider removes one provider's row; a missing row is no error.
	DeleteAuthProvider(ctx context.Context, projectID, provider string) error
}

// ProjectAuthSignInStore reads and writes the sign-in policy columns of the
// auth settings row (EXC-662) alongside the original two settings.
type ProjectAuthSignInStore interface {
	// GetAuthSignIn returns the policy; a project with no row reads as
	// domain.DefaultAuthSignIn().
	GetAuthSignIn(ctx context.Context, projectID string) (domain.ProjectAuthSignIn, error)
	// SetAuthSettingsWithSignIn upserts the settings and the policy together.
	SetAuthSettingsWithSignIn(ctx context.Context, projectID string, settings domain.ProjectAuthSettings, signIn domain.ProjectAuthSignIn) error
}
