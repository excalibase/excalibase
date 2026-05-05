package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
)

func (s *Store) CreateToken(ctx context.Context, t *domain.AccessToken) error {
	createdAt := time.Now().UTC().Format(time.RFC3339)
	if t.CreatedAt != nil {
		createdAt = t.CreatedAt.UTC().Format(time.RFC3339)
	}
	var expiresAt sql.NullString
	if t.ExpiresAt != nil {
		expiresAt = sql.NullString{Valid: true, String: t.ExpiresAt.UTC().Format(time.RFC3339)}
	}
	var scopes sql.NullString
	if t.Scopes != "" {
		scopes = sql.NullString{Valid: true, String: t.Scopes}
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO access_tokens (token_hash, token_prefix, user_id, name, created_at, expires_at, scopes)
		VALUES (?,?,?,?,?,?,?)`,
		t.TokenHash, t.TokenPrefix, t.UserID, t.Name, createdAt, expiresAt, scopes)
	return err
}

func (s *Store) FindByTokenHash(ctx context.Context, hash string) (*domain.AccessToken, error) {
	row := s.db.QueryRowContext(ctx, `SELECT token_hash, token_prefix, user_id, name, created_at, expires_at, last_used, scopes FROM access_tokens WHERE token_hash = ?`, hash)
	t, err := scanToken(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return t, nil
}

func (s *Store) ListTokensByUser(ctx context.Context, userID string) ([]*domain.AccessToken, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT token_hash, token_prefix, user_id, name, created_at, expires_at, last_used, scopes FROM access_tokens WHERE user_id = ?`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := make([]*domain.AccessToken, 0)
	for rows.Next() {
		t, err := scanToken(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, t)
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

// rowScanner is the common shape of *sql.Row and *sql.Rows for Scan.
type rowScanner interface {
	Scan(dest ...interface{}) error
}

func scanToken(r rowScanner) (*domain.AccessToken, error) {
	var t domain.AccessToken
	var createdAt, expiresAt, lastUsed, scopes sql.NullString
	if err := r.Scan(&t.TokenHash, &t.TokenPrefix, &t.UserID, &t.Name, &createdAt, &expiresAt, &lastUsed, &scopes); err != nil {
		return nil, err
	}
	if createdAt.Valid {
		if ts, err := time.Parse(time.RFC3339, createdAt.String); err == nil {
			t.CreatedAt = &ts
		}
	}
	if expiresAt.Valid {
		if ts, err := time.Parse(time.RFC3339, expiresAt.String); err == nil {
			t.ExpiresAt = &ts
		}
	}
	if lastUsed.Valid {
		if ts, err := time.Parse(time.RFC3339, lastUsed.String); err == nil {
			t.LastUsed = &ts
		}
	}
	if scopes.Valid {
		t.Scopes = scopes.String
	}
	return &t, nil
}
