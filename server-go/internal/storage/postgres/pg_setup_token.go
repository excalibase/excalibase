package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/storage"
)

// HasPlatformAdmin reports whether any user already holds the platform_admin
// role. See storage.SetupTokenStore.
func (s *Store) HasPlatformAdmin(ctx context.Context) (bool, error) {
	var exists bool
	err := s.db.QueryRowContext(ctx,
		`SELECT EXISTS(SELECT 1 FROM users WHERE role = 'platform_admin')`).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("check platform admin: %w", err)
	}
	return exists, nil
}

// StoreSetupTokenHash replaces any previously stored one-time setup token
// hash with tokenHash. See storage.SetupTokenStore.
func (s *Store) StoreSetupTokenHash(ctx context.Context, tokenHash string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin store setup token: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // rollback after commit is a no-op

	if _, err := tx.ExecContext(ctx, `DELETE FROM setup_tokens`); err != nil {
		return fmt.Errorf("clear setup tokens: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO setup_tokens (token_hash, created_at) VALUES ($1, $2)`,
		tokenHash, time.Now().UTC()); err != nil {
		return fmt.Errorf("store setup token: %w", err)
	}
	return tx.Commit()
}

// CreateFirstAdmin verifies tokenHash against the stored one-time token and,
// only on a match, creates user and burns the token in the same transaction.
// See storage.SetupTokenStore.
func (s *Store) CreateFirstAdmin(ctx context.Context, tokenHash string, user *domain.User) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin create first admin: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // rollback after commit is a no-op

	// DELETE takes a row lock: a second transaction racing with the same
	// tokenHash blocks here until the first commits or rolls back, then finds
	// zero rows itself — so at most one caller ever proceeds past this point.
	result, err := tx.ExecContext(ctx, `DELETE FROM setup_tokens WHERE token_hash = $1`, tokenHash)
	if err != nil {
		return fmt.Errorf("burn setup token: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("burn setup token: %w", err)
	}
	if rows == 0 {
		return storage.ErrInvalidSetupToken
	}

	now := time.Now().UTC()
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO users (id, username, email, password_hash, role, active, kind, created_at, updated_at)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`,
		user.ID, user.Username, user.Email, user.PasswordHash, user.Role, user.Active, userKind(user), now, now); err != nil {
		return fmt.Errorf("create first admin: %w", err)
	}

	return tx.Commit()
}
