package postgres

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/lib/pq"
)

const tokenColumns = `token_hash, token_prefix, user_id, name, created_at, expires_at, last_used, scopes, project_id, permissions`

func (s *Store) CreateToken(ctx context.Context, t *domain.AccessToken) error {
	createdAt := time.Now().UTC()
	if t.CreatedAt != nil {
		createdAt = t.CreatedAt.UTC()
	}
	var expiresAt sql.NullTime
	if t.ExpiresAt != nil {
		expiresAt = sql.NullTime{Valid: true, Time: t.ExpiresAt.UTC()}
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO access_tokens (token_hash, token_prefix, user_id, name, created_at, expires_at, scopes, project_id, permissions)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`,
		t.TokenHash, t.TokenPrefix, t.UserID, t.Name, createdAt, expiresAt, nullString(t.Scopes), nullString(t.ProjectID), pq.Array(t.Permissions))
	return err
}

func (s *Store) FindByTokenHash(ctx context.Context, hash string) (*domain.AccessToken, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT `+tokenColumns+` FROM access_tokens WHERE token_hash = $1`, hash)
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
		`SELECT `+tokenColumns+` FROM access_tokens WHERE user_id = $1`, userID)
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

func (s *Store) UpdateTokenExpiry(ctx context.Context, tokenHash string, expiresAt *time.Time) error {
	var value sql.NullTime
	if expiresAt != nil {
		value = sql.NullTime{Valid: true, Time: expiresAt.UTC()}
	}
	_, err := s.db.ExecContext(ctx,
		`UPDATE access_tokens SET expires_at = $2 WHERE token_hash = $1`, tokenHash, value)
	return err
}

func (s *Store) TouchTokenLastUsed(ctx context.Context, tokenHash string, at time.Time) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE access_tokens SET last_used = $2 WHERE token_hash = $1`, tokenHash, at.UTC())
	return err
}

type pgRowScanner interface {
	Scan(dest ...interface{}) error
}

// nullString maps "" to SQL NULL so optional text columns stay NULL rather
// than empty strings.
func nullString(v string) sql.NullString {
	return sql.NullString{Valid: v != "", String: v}
}

func scanPgToken(r pgRowScanner) (*domain.AccessToken, error) {
	var t domain.AccessToken
	var createdAt, expiresAt, lastUsed sql.NullTime
	var scopes, projectID sql.NullString
	var permissions pq.StringArray
	if err := r.Scan(&t.TokenHash, &t.TokenPrefix, &t.UserID, &t.Name, &createdAt, &expiresAt, &lastUsed, &scopes, &projectID, &permissions); err != nil {
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
	t.Scopes = scopes.String
	t.ProjectID = projectID.String
	if len(permissions) > 0 {
		t.Permissions = []string(permissions)
	}
	return &t, nil
}
