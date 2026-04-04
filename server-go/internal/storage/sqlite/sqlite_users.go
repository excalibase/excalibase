package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
)

func (s *Store) CreateUser(ctx context.Context, u *domain.User) error {
	now := time.Now().Format(time.RFC3339)
	_, err := s.db.ExecContext(ctx, `INSERT INTO users (id, username, email, password_hash, role, active, created_at, updated_at)
		VALUES (?,?,?,?,?,?,?,?)`, u.ID, u.Username, u.Email, u.PasswordHash, u.Role, u.Active, now, now)
	return err
}

func (s *Store) UpdateUserPassword(ctx context.Context, username, passwordHash string) error {
	now := time.Now().Format(time.RFC3339)
	result, err := s.db.ExecContext(ctx, `UPDATE users SET password_hash = ?, updated_at = ? WHERE username = ?`, passwordHash, now, username)
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
	row := s.db.QueryRowContext(ctx, `SELECT id, username, email, password_hash, role, active, created_at, updated_at FROM users WHERE id = ?`, id)
	return scanUser(row)
}

func (s *Store) FindUserByUsername(ctx context.Context, username string) (*domain.User, error) {
	row := s.db.QueryRowContext(ctx, `SELECT id, username, email, password_hash, role, active, created_at, updated_at FROM users WHERE username = ?`, username)
	return scanUser(row)
}

func (s *Store) FindAllUsers(ctx context.Context) ([]*domain.User, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, username, email, password_hash, role, active, created_at, updated_at FROM users`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []*domain.User
	for rows.Next() {
		var u domain.User
		var createdAt, updatedAt sql.NullString
		if err := rows.Scan(&u.ID, &u.Username, &u.Email, &u.PasswordHash, &u.Role, &u.Active, &createdAt, &updatedAt); err != nil {
			return nil, err
		}
		result = append(result, &u)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate rows: %w", err)
	}
	if result == nil {
		result = make([]*domain.User, 0)
	}
	return result, nil
}

func (s *Store) DeleteUser(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM users WHERE id = ?`, id)
	return err
}

func scanUser(row *sql.Row) (*domain.User, error) {
	var u domain.User
	var createdAt, updatedAt sql.NullString
	err := row.Scan(&u.ID, &u.Username, &u.Email, &u.PasswordHash, &u.Role, &u.Active, &createdAt, &updatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &u, nil
}
