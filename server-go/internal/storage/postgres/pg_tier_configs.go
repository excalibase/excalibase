package postgres

import (
	"context"
	"database/sql"
	"errors"

	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/domain"
)

func (s *Store) ListTierConfigs(ctx context.Context) (map[domain.TierType]config.TierConfig, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT tier, max_projects, instances, storage_size, memory, cpu, backup_enabled FROM tier_configs`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make(map[domain.TierType]config.TierConfig)
	for rows.Next() {
		tier, tc, err := scanTierConfig(rows)
		if err != nil {
			return nil, err
		}
		out[tier] = tc
	}
	return out, rows.Err()
}

func (s *Store) GetTierConfig(ctx context.Context, tier domain.TierType) (config.TierConfig, bool, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT tier, max_projects, instances, storage_size, memory, cpu, backup_enabled
		 FROM tier_configs WHERE tier = $1`, tier)
	_, tc, err := scanTierConfig(row)
	if errors.Is(err, sql.ErrNoRows) {
		return config.TierConfig{}, false, nil
	}
	if err != nil {
		return config.TierConfig{}, false, err
	}
	return tc, true, nil
}

func (s *Store) UpsertTierConfig(ctx context.Context, tier domain.TierType, tc config.TierConfig) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO tier_configs (tier, max_projects, instances, storage_size, memory, cpu, backup_enabled, updated_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, NOW())
		 ON CONFLICT (tier) DO UPDATE SET
		    max_projects   = EXCLUDED.max_projects,
		    instances      = EXCLUDED.instances,
		    storage_size   = EXCLUDED.storage_size,
		    memory         = EXCLUDED.memory,
		    cpu            = EXCLUDED.cpu,
		    backup_enabled = EXCLUDED.backup_enabled,
		    updated_at     = NOW()`,
		tier, tc.MaxProjects, tc.Instances, tc.StorageSize, tc.Memory, tc.CPU, tc.BackupEnabled)
	return err
}

type tierRowScanner interface{ Scan(dest ...any) error }

func scanTierConfig(r tierRowScanner) (domain.TierType, config.TierConfig, error) {
	var tier domain.TierType
	var tc config.TierConfig
	err := r.Scan(&tier, &tc.MaxProjects, &tc.Instances, &tc.StorageSize, &tc.Memory, &tc.CPU, &tc.BackupEnabled)
	return tier, tc, err
}
