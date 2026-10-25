package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/lib/pq"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/storage"
)

// ListAuthProviders returns the project's stored provider rows.
func (s *Store) ListAuthProviders(ctx context.Context, projectID string) ([]domain.ProjectAuthProvider, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT provider, enabled, client_id, client_secret_set, updated_at
FROM project_auth_providers WHERE project_id = $1 ORDER BY provider`, projectID)
	if err != nil {
		return nil, fmt.Errorf("read auth providers: %w", err)
	}
	defer rows.Close()
	out := []domain.ProjectAuthProvider{}
	for rows.Next() {
		var provider domain.ProjectAuthProvider
		if err := rows.Scan(&provider.Provider, &provider.Enabled, &provider.ClientID, &provider.ClientSecretSet, &provider.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan auth provider: %w", err)
		}
		out = append(out, provider)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read auth providers: %w", err)
	}
	return out, nil
}

// PutAuthProvider upserts one provider's row.
func (s *Store) PutAuthProvider(ctx context.Context, projectID string, provider domain.ProjectAuthProvider) error {
	_, err := s.db.ExecContext(ctx, `
INSERT INTO project_auth_providers (project_id, provider, enabled, client_id, client_secret_set, updated_at)
VALUES ($1, $2, $3, $4, $5, $6)
ON CONFLICT (project_id, provider) DO UPDATE SET
    enabled           = EXCLUDED.enabled,
    client_id         = EXCLUDED.client_id,
    client_secret_set = EXCLUDED.client_secret_set,
    updated_at        = EXCLUDED.updated_at`,
		projectID, provider.Provider, provider.Enabled, provider.ClientID, provider.ClientSecretSet, time.Now())
	if err != nil {
		return fmt.Errorf("write auth provider: %w", err)
	}
	return nil
}

// DeleteAuthProvider removes one provider's row.
func (s *Store) DeleteAuthProvider(ctx context.Context, projectID, provider string) error {
	if _, err := s.db.ExecContext(ctx,
		`DELETE FROM project_auth_providers WHERE project_id = $1 AND provider = $2`, projectID, provider); err != nil {
		return fmt.Errorf("delete auth provider: %w", err)
	}
	return nil
}

// GetAuthSignIn reads the sign-in policy; no row reads as the default.
func (s *Store) GetAuthSignIn(ctx context.Context, projectID string) (domain.ProjectAuthSignIn, error) {
	signIn := domain.DefaultAuthSignIn()
	var urls []string
	err := s.db.QueryRowContext(ctx,
		`SELECT redirect_urls, allow_new_users FROM project_auth_settings WHERE project_id = $1`, projectID).
		Scan(pq.Array(&urls), &signIn.AllowNewUsers)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.DefaultAuthSignIn(), nil
	}
	if err != nil {
		return domain.ProjectAuthSignIn{}, fmt.Errorf("read auth sign-in settings: %w", err)
	}
	if urls != nil {
		signIn.RedirectURLs = urls
	}
	return signIn, nil
}

// SetAuthSettingsWithSignIn upserts the settings and the policy in one statement.
func (s *Store) SetAuthSettingsWithSignIn(ctx context.Context, projectID string, settings domain.ProjectAuthSettings, signIn domain.ProjectAuthSignIn) error {
	urls := signIn.RedirectURLs
	if urls == nil {
		urls = []string{}
	}
	_, err := s.db.ExecContext(ctx, `
INSERT INTO project_auth_settings (project_id, require_email_verification, site_url, redirect_urls, allow_new_users, updated_at)
VALUES ($1, $2, $3, $4, $5, $6)
ON CONFLICT (project_id) DO UPDATE SET
    require_email_verification = EXCLUDED.require_email_verification,
    site_url                   = EXCLUDED.site_url,
    redirect_urls              = EXCLUDED.redirect_urls,
    allow_new_users            = EXCLUDED.allow_new_users,
    updated_at                 = EXCLUDED.updated_at`,
		projectID, settings.RequireEmailVerification, settings.SiteURL, pq.Array(urls), signIn.AllowNewUsers, time.Now())
	if err != nil {
		return fmt.Errorf("write auth settings: %w", err)
	}
	return nil
}

var (
	_ storage.ProjectAuthProviderStore = (*Store)(nil)
	_ storage.ProjectAuthSignInStore   = (*Store)(nil)
)
