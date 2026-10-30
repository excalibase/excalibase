package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/excalibase/provisioning-poc/internal/storage"
)

// EnableMFA upserts only the switch. No lock or transaction is held across
// the caller's vault calls, which share this connection pool.
func (s *Store) EnableMFA(ctx context.Context, projectID string) error {
	if _, err := s.db.ExecContext(ctx, `
INSERT INTO project_auth_settings (project_id, mfa_enabled, updated_at) VALUES ($1, true, $2)
ON CONFLICT (project_id) DO UPDATE SET mfa_enabled = true, updated_at = EXCLUDED.updated_at`,
		projectID, time.Now()); err != nil {
		return fmt.Errorf("write mfa setting: %w", err)
	}
	return nil
}

// DisableMFA updates only an existing row: no row already means off.
func (s *Store) DisableMFA(ctx context.Context, projectID string) error {
	if _, err := s.db.ExecContext(ctx,
		`UPDATE project_auth_settings SET mfa_enabled = false, updated_at = $2 WHERE project_id = $1`, projectID, time.Now()); err != nil {
		return fmt.Errorf("write mfa setting: %w", err)
	}
	return nil
}

var _ storage.ProjectAuthMFAStore = (*Store)(nil)
