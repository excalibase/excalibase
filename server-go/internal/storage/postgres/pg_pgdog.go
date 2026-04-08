package postgres

import (
	"context"
	"fmt"

	"github.com/excalibase/provisioning-poc/internal/domain"
)

func (s *Store) RegisterPgDogDatabase(ctx context.Context, db *domain.PgDogDatabase) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO pgdog_databases (name, host, port, database_name, role, shard, pool_size, read_only, active)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, TRUE)
		ON CONFLICT DO NOTHING`,
		db.Name, db.Host, db.Port, db.DatabaseName, db.Role, db.Shard, db.PoolSize, db.ReadOnly)
	if err != nil {
		return fmt.Errorf("register pgdog database: %w", err)
	}
	return nil
}

func (s *Store) RemovePgDogDatabase(ctx context.Context, name string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM pgdog_databases WHERE name = $1`, name)
	if err != nil {
		return fmt.Errorf("remove pgdog database: %w", err)
	}
	return nil
}

func (s *Store) RegisterPgDogUser(ctx context.Context, user *domain.PgDogUser) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO pgdog_users (name, database, password, pool_size, active)
		VALUES ($1, $2, $3, $4, TRUE)
		ON CONFLICT (name, database) DO UPDATE SET password = EXCLUDED.password`,
		user.Name, user.Database, user.Password, user.PoolSize)
	if err != nil {
		return fmt.Errorf("register pgdog user: %w", err)
	}
	return nil
}

func (s *Store) RemovePgDogUser(ctx context.Context, name, database string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM pgdog_users WHERE name = $1 AND database = $2`, name, database)
	if err != nil {
		return fmt.Errorf("remove pgdog user: %w", err)
	}
	return nil
}
