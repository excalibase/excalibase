package postgres

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/excalibase/provisioning-poc/internal/storage"
)

// copyRestoredSettingsSQL copies, in dependency order, what decides who may
// reach a project through its API. CORS origins an app added stay behind: the
// app is not part of the copy. OAuth providers stay behind too: the copy has
// its own callback URL and no client secret in the vault. $1 is the source,
// $2 the copy.
var copyRestoredSettingsSQL = []struct{ what, query string }{
	{"table permissions", `
		INSERT INTO api_permissions (project_id, table_key, role_name, operation, definition)
		SELECT $2, table_key, role_name, operation, definition FROM api_permissions WHERE project_id = $1`},
	{"tracked functions", `
		INSERT INTO tracked_functions (project_id, function_key, exposed_as, infer_permissions, session_argument)
		SELECT $2, function_key, exposed_as, infer_permissions, session_argument FROM tracked_functions WHERE project_id = $1`},
	{"function permissions", `
		INSERT INTO function_permissions (project_id, function_key, role_name)
		SELECT $2, function_key, role_name FROM function_permissions WHERE project_id = $1`},
	{"cors origins", `
		INSERT INTO project_cors_settings (project_id, allowed_origins)
		SELECT $2, ARRAY(
			SELECT o.origin FROM unnest(c.allowed_origins) WITH ORDINALITY AS o(origin, n)
			WHERE NOT EXISTS (
				SELECT 1 FROM jsonb_each(c.app_origins) AS app(id, origins), jsonb_array_elements_text(app.origins) AS added
				WHERE added = o.origin)
			ORDER BY o.n)
		FROM project_cors_settings c WHERE c.project_id = $1`},
	{"auth settings", `
		INSERT INTO project_auth_settings (project_id, require_email_verification, site_url, redirect_urls, allow_new_users)
		SELECT $2, require_email_verification, site_url, redirect_urls, allow_new_users FROM project_auth_settings WHERE project_id = $1`},
}

// CopyRestoredSettings carries the source's API settings onto a restored copy
// in one transaction. See storage.RestoredSettingsStore.
func (s *Store) CopyRestoredSettings(ctx context.Context, sourceID, targetID string) error {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead})
	if err != nil {
		return fmt.Errorf("begin restored settings copy: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // rollback after a commit is a no-op

	for _, step := range copyRestoredSettingsSQL {
		if _, err := tx.ExecContext(ctx, step.query, sourceID, targetID); err != nil {
			return fmt.Errorf("copy %s: %w", step.what, err)
		}
	}
	if err := bumpVersion(ctx, tx, targetID); err != nil {
		return fmt.Errorf("version the copied permissions: %w", err)
	}
	return tx.Commit()
}

var _ storage.RestoredSettingsStore = (*Store)(nil)
