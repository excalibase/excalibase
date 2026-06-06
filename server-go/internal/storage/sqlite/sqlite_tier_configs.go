package sqlite

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
		 FROM tier_configs WHERE tier = ?`, tier)
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
	backup := 0
	if tc.BackupEnabled {
		backup = 1
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO tier_configs (tier, max_projects, instances, storage_size, memory, cpu, backup_enabled, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, CURRENT_TIMESTAMP)
		 ON CONFLICT(tier) DO UPDATE SET
		    max_projects   = excluded.max_projects,
		    instances      = excluded.instances,
		    storage_size   = excluded.storage_size,
		    memory         = excluded.memory,
		    cpu            = excluded.cpu,
		    backup_enabled = excluded.backup_enabled,
		    updated_at     = CURRENT_TIMESTAMP`,
		tier, tc.MaxProjects, tc.Instances, tc.StorageSize, tc.Memory, tc.CPU, backup)
	return err
}

type tierRowScanner interface{ Scan(dest ...any) error }

// scanTierConfig reads backup_enabled as an int (SQLite has no bool) and
// converts to the config.TierConfig bool field.
func scanTierConfig(r tierRowScanner) (domain.TierType, config.TierConfig, error) {
	var tier domain.TierType
	var tc config.TierConfig
	var backup int
	err := r.Scan(&tier, &tc.MaxProjects, &tc.Instances, &tc.StorageSize, &tc.Memory, &tc.CPU, &backup)
	tc.BackupEnabled = backup != 0
	return tier, tc, err
}
