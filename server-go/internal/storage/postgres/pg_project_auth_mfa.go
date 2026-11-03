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

// MFAKeyFingerprint reads the recorded key fingerprint; no row or none recorded is "".
func (s *Store) MFAKeyFingerprint(ctx context.Context, projectID string) (string, error) {
	var fingerprint sql.NullString
	err := s.db.QueryRowContext(ctx,
		`SELECT mfa_key_fingerprint FROM project_auth_settings WHERE project_id = $1`, projectID).Scan(&fingerprint)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("read mfa key fingerprint: %w", err)
	}
	return fingerprint.String, nil
}

// EnableMFA upserts only the switch and the fingerprint, never over another
// key's. No lock or transaction is held across the caller's vault calls,
// which share this connection pool.
func (s *Store) EnableMFA(ctx context.Context, projectID, keyFingerprint string) error {
	result, err := s.db.ExecContext(ctx, `
INSERT INTO project_auth_settings (project_id, mfa_enabled, mfa_key_fingerprint, updated_at) VALUES ($1, true, $2, $3)
ON CONFLICT (project_id) DO UPDATE SET mfa_enabled = true, mfa_key_fingerprint = EXCLUDED.mfa_key_fingerprint, updated_at = EXCLUDED.updated_at
WHERE project_auth_settings.mfa_key_fingerprint IS NULL OR project_auth_settings.mfa_key_fingerprint = EXCLUDED.mfa_key_fingerprint`,
		projectID, keyFingerprint, time.Now())
	if err != nil {
		return fmt.Errorf("write mfa setting: %w", err)
	}
	if written, err := result.RowsAffected(); err != nil {
		return fmt.Errorf("write mfa setting: %w", err)
	} else if written == 0 {
		return domain.ErrMFAKeyLost
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
