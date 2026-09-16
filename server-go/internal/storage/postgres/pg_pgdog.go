package postgres

import (
	"context"
	"fmt"

	"github.com/excalibase/provisioning-poc/internal/domain"
)

// RegisterPgDogDatabase upserts one route (name, role, shard). A re-register
// after a host move updates the row in place instead of stacking duplicates
// that PgDog would load as extra shards.
func (s *Store) RegisterPgDogDatabase(ctx context.Context, db *domain.PgDogDatabase) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO pgdog_databases (name, host, port, database_name, role, shard, pool_size, read_only, active)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, TRUE)
		ON CONFLICT (name, role, shard) DO UPDATE SET
			host = EXCLUDED.host,
			port = EXCLUDED.port,
			database_name = EXCLUDED.database_name,
			pool_size = EXCLUDED.pool_size,
			read_only = EXCLUDED.read_only,
			active = TRUE,
			updated_at = NOW()`,
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
		ON CONFLICT (name, database) DO UPDATE SET
			password = EXCLUDED.password,
			pool_size = EXCLUDED.pool_size,
			active = TRUE`,
		user.Name, user.Database, user.Password, user.PoolSize)
	if err != nil {
		return fmt.Errorf("register pgdog user: %w", err)
	}
	return nil
}

func (s *Store) RemovePgDogUsers(ctx context.Context, database string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM pgdog_users WHERE database = $1`, database)
	if err != nil {
		return fmt.Errorf("remove pgdog users: %w", err)
	}
	return nil
}
