package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
)

func (s *Store) CreateToken(ctx context.Context, t *domain.AccessToken) error {
	now := time.Now().Format(time.RFC3339)
	_, err := s.db.ExecContext(ctx, `INSERT INTO access_tokens (token_hash, token_prefix, user_id, name, created_at, expires_at)
		VALUES (?,?,?,?,?,?)`, t.TokenHash, t.TokenPrefix, t.UserID, t.Name, now, nil)
	return err
}

func (s *Store) FindByTokenHash(ctx context.Context, hash string) (*domain.AccessToken, error) {
	row := s.db.QueryRowContext(ctx, `SELECT token_hash, token_prefix, user_id, name, created_at, expires_at, last_used FROM access_tokens WHERE token_hash = ?`, hash)
	var t domain.AccessToken
	var createdAt, expiresAt, lastUsed sql.NullString
	err := row.Scan(&t.TokenHash, &t.TokenPrefix, &t.UserID, &t.Name, &createdAt, &expiresAt, &lastUsed)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &t, nil
}

func (s *Store) ListTokensByUser(ctx context.Context, userID string) ([]*domain.AccessToken, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT token_hash, token_prefix, user_id, name, created_at, expires_at, last_used FROM access_tokens WHERE user_id = ?`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := make([]*domain.AccessToken, 0)
	for rows.Next() {
		var t domain.AccessToken
		var createdAt, expiresAt, lastUsed sql.NullString
		if err := rows.Scan(&t.TokenHash, &t.TokenPrefix, &t.UserID, &t.Name, &createdAt, &expiresAt, &lastUsed); err != nil {
			return nil, err
		}
		result = append(result, &t)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate rows: %w", err)
	}
	return result, nil
}

func (s *Store) DeleteToken(ctx context.Context, tokenHash string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM access_tokens WHERE token_hash = ?`, tokenHash)
	return err
}
