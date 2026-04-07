package postgres

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
)

func (s *Store) CreateUser(ctx context.Context, u *domain.User) error {
	now := time.Now().UTC()
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO users (id, username, email, password_hash, role, active, created_at, updated_at)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`,
		u.ID, u.Username, u.Email, u.PasswordHash, u.Role, u.Active, now, now)
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
		`SELECT id, username, email, password_hash, role, active, created_at, updated_at FROM users WHERE id = $1`, id)
	return scanUser(row)
}

func (s *Store) FindUserByUsername(ctx context.Context, username string) (*domain.User, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, username, email, password_hash, role, active, created_at, updated_at FROM users WHERE username = $1`, username)
	return scanUser(row)
}

func (s *Store) FindUserByEmail(ctx context.Context, email string) (*domain.User, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, username, email, password_hash, role, active, created_at, updated_at FROM users WHERE email = $1`, email)
	return scanUser(row)
}

func (s *Store) FindAllUsers(ctx context.Context) ([]*domain.User, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, username, email, password_hash, role, active, created_at, updated_at FROM users`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := make([]*domain.User, 0)
	for rows.Next() {
		var u domain.User
		var createdAt, updatedAt sql.NullTime
		if err := rows.Scan(&u.ID, &u.Username, &u.Email, &u.PasswordHash, &u.Role, &u.Active, &createdAt, &updatedAt); err != nil {
			return nil, err
		}
		result = append(result, &u)
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

func scanUser(row *sql.Row) (*domain.User, error) {
	var u domain.User
	var createdAt, updatedAt sql.NullTime
	err := row.Scan(&u.ID, &u.Username, &u.Email, &u.PasswordHash, &u.Role, &u.Active, &createdAt, &updatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &u, nil
}
