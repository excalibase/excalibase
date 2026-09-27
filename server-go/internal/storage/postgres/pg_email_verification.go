package postgres

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/excalibase/provisioning-poc/internal/storage"
)

func (s *Store) CreateEmailVerification(ctx context.Context, userID, email, tokenHash string, expiresAt time.Time) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO email_verifications (user_id, email, token_hash, expires_at) VALUES ($1, $2, $3, $4)`,
		userID, email, tokenHash, expiresAt.UTC())
	return err
}

func (s *Store) ConsumeEmailVerification(ctx context.Context, tokenHash string, now time.Time) (string, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer func() { _ = tx.Rollback() }()

	var userID, email string
	err = tx.QueryRowContext(ctx,
		`UPDATE email_verifications SET consumed_at = $2
		 WHERE token_hash = $1 AND consumed_at IS NULL AND expires_at > $2
		 RETURNING user_id, email`, tokenHash, now.UTC()).Scan(&userID, &email)
	if errors.Is(err, sql.ErrNoRows) {
		return "", storage.ErrEmailVerificationInvalid
	}
	if err != nil {
		return "", err
	}
	res, err := tx.ExecContext(ctx,
		`UPDATE users SET email_verified_at = COALESCE(email_verified_at, $3), updated_at = $3
		 WHERE id = $1 AND email = $2`, userID, email, now.UTC())
	if err != nil {
		return "", err
	}
	if updated, err := res.RowsAffected(); err != nil || updated == 0 {
		return "", errors.Join(storage.ErrEmailVerificationInvalid, err)
	}
	return userID, tx.Commit()
}

func (s *Store) MarkEmailVerified(ctx context.Context, userID string, now time.Time) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE users SET email_verified_at = COALESCE(email_verified_at, $2), updated_at = $2 WHERE id = $1`,
		userID, now.UTC())
	if err != nil {
		return err
	}
	if updated, err := res.RowsAffected(); err != nil || updated == 0 {
		return errors.Join(storage.ErrUserNotFound, err)
	}
	return nil
}
