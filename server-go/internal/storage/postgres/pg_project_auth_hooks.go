package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/storage"
)

// ListAuthHooks returns the project's stored hook rows.
func (s *Store) ListAuthHooks(ctx context.Context, projectID string) ([]domain.ProjectAuthHook, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT hook, enabled, url, updated_at FROM project_auth_hooks WHERE project_id = $1 ORDER BY hook`, projectID)
	if err != nil {
		return nil, fmt.Errorf("read auth hooks: %w", err)
	}
	defer rows.Close()
	out := []domain.ProjectAuthHook{}
	for rows.Next() {
		var hook domain.ProjectAuthHook
		if err := rows.Scan(&hook.Hook, &hook.Enabled, &hook.URL, &hook.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan auth hook: %w", err)
		}
		out = append(out, hook)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read auth hooks: %w", err)
	}
	return out, nil
}

// putAuthHookSQL writes an enabled hook only while the secret row exists. FOR
// SHARE makes a concurrent ClearAuthHookSecret wait for this write, or this
// write see the row gone, so a hook is never left on without its secret.
const putAuthHookSQL = `
INSERT INTO project_auth_hooks (project_id, hook, enabled, url, updated_at)
SELECT $1, $2, $3, $4, $5
WHERE NOT $3::boolean OR EXISTS (SELECT 1 FROM project_auth_hook_secrets WHERE project_id = $1 FOR SHARE)
ON CONFLICT (project_id, hook) DO UPDATE SET
    enabled    = EXCLUDED.enabled,
    url        = EXCLUDED.url,
    updated_at = EXCLUDED.updated_at`

// PutAuthHook upserts one hook; see storage.ProjectAuthHookStore.
func (s *Store) PutAuthHook(ctx context.Context, projectID string, hook domain.ProjectAuthHook) error {
	result, err := s.db.ExecContext(ctx, putAuthHookSQL, projectID, hook.Hook, hook.Enabled, hook.URL, time.Now())
	if err != nil {
		return fmt.Errorf("write auth hook: %w", err)
	}
	written, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("write auth hook: %w", err)
	}
	if written == 0 {
		return domain.ErrAuthHookNeedsSecret
	}
	return nil
}

// AuthHookSecretSet reports whether the project's secret row exists.
func (s *Store) AuthHookSecretSet(ctx context.Context, projectID string) (bool, error) {
	var set bool
	err := s.db.QueryRowContext(ctx,
		`SELECT EXISTS (SELECT 1 FROM project_auth_hook_secrets WHERE project_id = $1)`, projectID).Scan(&set)
	if err != nil {
		return false, fmt.Errorf("read auth hook secret state: %w", err)
	}
	return set, nil
}

// MarkAuthHookSecretSet records a rotation; the vault write is the caller's and comes first.
func (s *Store) MarkAuthHookSecretSet(ctx context.Context, projectID string) error {
	_, err := s.db.ExecContext(ctx, `
INSERT INTO project_auth_hook_secrets (project_id, rotated_at) VALUES ($1, $2)
ON CONFLICT (project_id) DO UPDATE SET rotated_at = EXCLUDED.rotated_at`, projectID, time.Now())
	if err != nil {
		return fmt.Errorf("write auth hook secret state: %w", err)
	}
	return nil
}

// ClearAuthHookSecret forgets the secret and switches every hook off in one
// transaction; the vault delete is the caller's and comes after.
func (s *Store) ClearAuthHookSecret(ctx context.Context, projectID string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin clear auth hook secret: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // rollback after a commit is a no-op

	if _, err := tx.ExecContext(ctx, `DELETE FROM project_auth_hook_secrets WHERE project_id = $1`, projectID); err != nil {
		return fmt.Errorf("clear auth hook secret state: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE project_auth_hooks SET enabled = false, updated_at = $2 WHERE project_id = $1 AND enabled`, projectID, time.Now()); err != nil {
		return fmt.Errorf("switch off auth hooks: %w", err)
	}
	return tx.Commit()
}

var _ storage.ProjectAuthHookStore = (*Store)(nil)
