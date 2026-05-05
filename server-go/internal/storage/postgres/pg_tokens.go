package postgres

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
)

func (s *Store) CreateToken(ctx context.Context, t *domain.AccessToken) error {
	createdAt := time.Now().UTC()
	if t.CreatedAt != nil {
		createdAt = t.CreatedAt.UTC()
	}
	var expiresAt sql.NullTime
	if t.ExpiresAt != nil {
		expiresAt = sql.NullTime{Valid: true, Time: t.ExpiresAt.UTC()}
	}
	var scopes sql.NullString
	if t.Scopes != "" {
		scopes = sql.NullString{Valid: true, String: t.Scopes}
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO access_tokens (token_hash, token_prefix, user_id, name, created_at, expires_at, scopes)
		 VALUES ($1,$2,$3,$4,$5,$6,$7)`,
		t.TokenHash, t.TokenPrefix, t.UserID, t.Name, createdAt, expiresAt, scopes)
	return err
}

func (s *Store) FindByTokenHash(ctx context.Context, hash string) (*domain.AccessToken, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT token_hash, token_prefix, user_id, name, created_at, expires_at, last_used, scopes
		 FROM access_tokens WHERE token_hash = $1`, hash)
	t, err := scanPgToken(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return t, nil
}

func (s *Store) ListTokensByUser(ctx context.Context, userID string) ([]*domain.AccessToken, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT token_hash, token_prefix, user_id, name, created_at, expires_at, last_used, scopes
		 FROM access_tokens WHERE user_id = $1`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := make([]*domain.AccessToken, 0)
	for rows.Next() {
		t, err := scanPgToken(rows)
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
	_, err := s.db.ExecContext(ctx, `DELETE FROM access_tokens WHERE token_hash = $1`, tokenHash)
	return err
}

type pgRowScanner interface {
	Scan(dest ...interface{}) error
}

func scanPgToken(r pgRowScanner) (*domain.AccessToken, error) {
	var t domain.AccessToken
	var createdAt, expiresAt, lastUsed sql.NullTime
	var scopes sql.NullString
	if err := r.Scan(&t.TokenHash, &t.TokenPrefix, &t.UserID, &t.Name, &createdAt, &expiresAt, &lastUsed, &scopes); err != nil {
		return nil, err
	}
	if createdAt.Valid {
		ts := createdAt.Time
		t.CreatedAt = &ts
	}
	if expiresAt.Valid {
		ts := expiresAt.Time
		t.ExpiresAt = &ts
	}
	if lastUsed.Valid {
		ts := lastUsed.Time
		t.LastUsed = &ts
	}
	if scopes.Valid {
		t.Scopes = scopes.String
	}
	return &t, nil
}
