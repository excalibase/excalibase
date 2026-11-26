package postgres

import (
	"context"
)

// UpsertNatsCredential stores (or rotates) one principal's bcrypt hash.
// Re-provisioning a project rotates the tenant watcher's credential, which
// invalidates whatever the old watcher pod was holding.
func (s *Store) UpsertNatsCredential(ctx context.Context, principal, projectID, passwordHash string) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO nats_credentials (principal, project_id, password_hash)
		 VALUES ($1, NULLIF($2, ''), $3)
		 ON CONFLICT (principal) DO UPDATE SET
		    project_id    = EXCLUDED.project_id,
		    password_hash = EXCLUDED.password_hash,
		    updated_at    = now()`,
		principal, projectID, passwordHash)
	return err
}

// DeleteNatsCredential revokes one principal.
func (s *Store) DeleteNatsCredential(ctx context.Context, principal string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM nats_credentials WHERE principal = $1`, principal)
	return err
}

// DeleteNatsCredentialsForProject revokes every credential a project holds.
// Called on deprovision so a deleted project's watcher can never reconnect.
func (s *Store) DeleteNatsCredentialsForProject(ctx context.Context, projectID string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM nats_credentials WHERE project_id = $1`, projectID)
	return err
}
