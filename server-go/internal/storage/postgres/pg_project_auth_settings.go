package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/storage"
)

const selectAuthSettings = `SELECT project_id, require_email_verification, site_url FROM project_auth_settings WHERE project_id = $1`

// GetAuthSettings returns the project's auth settings; a project without a
// row reads as the zero value with ok=false.
func (s *Store) GetAuthSettings(ctx context.Context, projectID string) (domain.ProjectAuthSettings, bool, error) {
	var (
		settings  domain.ProjectAuthSettings
		discarded string
	)
	err := s.db.QueryRowContext(ctx, selectAuthSettings, projectID).
		Scan(&discarded, &settings.RequireEmailVerification, &settings.SiteURL)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.ProjectAuthSettings{}, false, nil
	}
	if err != nil {
		return domain.ProjectAuthSettings{}, false, fmt.Errorf("read auth settings: %w", err)
	}
	return settings, true, nil
}

// SetAuthSettings upserts the project's auth settings.
func (s *Store) SetAuthSettings(ctx context.Context, projectID string, settings domain.ProjectAuthSettings) error {
	_, err := s.db.ExecContext(ctx, `
INSERT INTO project_auth_settings (project_id, require_email_verification, site_url, updated_at)
VALUES ($1, $2, $3, $4)
ON CONFLICT (project_id) DO UPDATE SET
    require_email_verification = EXCLUDED.require_email_verification,
    site_url                   = EXCLUDED.site_url,
    updated_at                 = EXCLUDED.updated_at`,
		projectID, settings.RequireEmailVerification, settings.SiteURL, time.Now())
	if err != nil {
		return fmt.Errorf("write auth settings: %w", err)
	}
	return nil
}

var _ storage.ProjectAuthSettingsStore = (*Store)(nil)
