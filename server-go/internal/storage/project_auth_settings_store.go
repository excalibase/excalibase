package storage

import (
	"context"

	"github.com/excalibase/provisioning-poc/internal/domain"
)

// ProjectAuthSettingsStore persists the per-project auth behavior served on
// GET /api/projects/{id}/info (EXC-367): whether the auth service requires
// email verification before login, and the site URL used to build
// redirect/callback links. domain.ValidateSiteURL runs before any write.
type ProjectAuthSettingsStore interface {
	// GetAuthSettings returns the stored settings and ok=false when the
	// project has no row yet (the zero value: verification off, no site URL).
	GetAuthSettings(ctx context.Context, projectID string) (domain.ProjectAuthSettings, bool, error)
	// SetAuthSettings upserts the project's auth settings.
	SetAuthSettings(ctx context.Context, projectID string, settings domain.ProjectAuthSettings) error
}
