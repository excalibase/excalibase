package postgres

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/excalibase/provisioning-poc/internal/storage"
)

// EnableMFA holds a per-project transaction lock across ensureKey and the
// upsert, so concurrent enables (any replica) see the key one at a time.
func (s *Store) EnableMFA(ctx context.Context, projectID string, ensureKey func() error) error {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return fmt.Errorf("begin mfa enable: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // rollback after a commit is a no-op

	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('project_auth_mfa:' || $1, 0))`, projectID); err != nil {
		return fmt.Errorf("lock mfa enable: %w", err)
	}
	if err := ensureKey(); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO project_auth_settings (project_id, mfa_enabled, updated_at) VALUES ($1, true, $2)
ON CONFLICT (project_id) DO UPDATE SET mfa_enabled = true, updated_at = EXCLUDED.updated_at`,
		projectID, time.Now()); err != nil {
		return fmt.Errorf("write mfa setting: %w", err)
	}
	return tx.Commit()
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
