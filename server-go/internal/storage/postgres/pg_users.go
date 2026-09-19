package postgres

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
)

// userColumns is the select list every user read shares, in scanUserRow order.
const userColumns = `id, username, email, password_hash, role, active, kind, created_at, updated_at`

func (s *Store) CreateUser(ctx context.Context, u *domain.User) error {
	now := time.Now().UTC()
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO users (id, username, email, password_hash, role, active, kind, created_at, updated_at)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`,
		u.ID, u.Username, u.Email, u.PasswordHash, u.Role, u.Active, userKind(u), now, now)
	return err
}

func (s *Store) UpdateUserPassword(ctx context.Context, username, passwordHash string) error {
	now := time.Now().UTC()
	result, err := s.db.ExecContext(ctx,
		`UPDATE users SET password_hash = $1, updated_at = $2 WHERE username = $3`,
		passwordHash, now, username)
	if err != nil {
		return err
	}
	rows, _ := result.RowsAffected()
	if rows == 0 {
		return fmt.Errorf("user %q not found", username)
	}
	return nil
}

func (s *Store) FindUserByID(ctx context.Context, id string) (*domain.User, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT `+userColumns+` FROM users WHERE id = $1`, id)
	return scanUser(row)
}

func (s *Store) FindUserByUsername(ctx context.Context, username string) (*domain.User, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT `+userColumns+` FROM users WHERE username = $1`, username)
	return scanUser(row)
}

func (s *Store) FindUserByEmail(ctx context.Context, email string) (*domain.User, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT `+userColumns+` FROM users WHERE email = $1`, email)
	return scanUser(row)
}

func (s *Store) FindAllUsers(ctx context.Context) ([]*domain.User, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+userColumns+` FROM users`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := make([]*domain.User, 0)
	for rows.Next() {
		u, err := scanUserRow(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, u)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate rows: %w", err)
	}
	return result, nil
}

func (s *Store) DeleteUser(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM users WHERE id = $1`, id)
	return err
}

// userKind defaults a blank kind to human so callers that predate the column
// keep creating human accounts.
func userKind(u *domain.User) string {
	if u.Kind == "" {
		return domain.UserKindHuman
	}
	return u.Kind
}

func scanUser(row *sql.Row) (*domain.User, error) {
	u, err := scanUserRow(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return u, nil
}

// scanUserRow reads one user in userColumns order.
func scanUserRow(r pgRowScanner) (*domain.User, error) {
	var u domain.User
	var kind sql.NullString
	var createdAt, updatedAt sql.NullTime
	if err := r.Scan(&u.ID, &u.Username, &u.Email, &u.PasswordHash, &u.Role, &u.Active, &kind, &createdAt, &updatedAt); err != nil {
		return nil, err
	}
	u.Kind = kind.String
	if u.Kind == "" {
		u.Kind = domain.UserKindHuman
	}
	if createdAt.Valid {
		ts := createdAt.Time
		u.CreatedAt = &ts
	}
	if updatedAt.Valid {
		ts := updatedAt.Time
		u.UpdatedAt = &ts
	}
	return &u, nil
}
