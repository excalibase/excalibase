package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/storage"
)

func (s *Store) ListTierConfigs(ctx context.Context) (map[domain.TierType]config.TierConfig, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT tier, max_projects, instances, storage_size, max_storage_size, max_app_disk_size, max_apps, memory, cpu, backup_enabled, auto_pause_after_days,
		max_sites, max_site_deploy_bytes, max_site_files, max_site_file_bytes, max_site_deploys_per_hour, max_site_custom_domains,
		site_build_cpu_millis, site_build_memory, site_build_disk, site_build_timeout_seconds, site_build_concurrency, version,
		max_server_sites, max_site_server_bundle_bytes FROM tier_configs`)
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
		`SELECT tier, max_projects, instances, storage_size, max_storage_size, max_app_disk_size, max_apps, memory, cpu, backup_enabled, auto_pause_after_days,
		max_sites, max_site_deploy_bytes, max_site_files, max_site_file_bytes, max_site_deploys_per_hour, max_site_custom_domains,
		site_build_cpu_millis, site_build_memory, site_build_disk, site_build_timeout_seconds, site_build_concurrency, version,
		max_server_sites, max_site_server_bundle_bytes FROM tier_configs WHERE tier = $1`, tier)
	_, tc, err := scanTierConfig(row)
	if errors.Is(err, sql.ErrNoRows) {
		return config.TierConfig{}, false, nil
	}
	if err != nil {
		return config.TierConfig{}, false, err
	}
	return tc, true, nil
}

// UpsertTierConfig writes the tier in one statement: the version check and the
// update cannot be split by another admin's save.
func (s *Store) UpsertTierConfig(ctx context.Context, tier domain.TierType, tc config.TierConfig, expectedVersion *int) (int, error) {
	var version int
	err := s.db.QueryRowContext(ctx,
		`INSERT INTO tier_configs (tier, max_projects, instances, storage_size, memory, cpu, backup_enabled, auto_pause_after_days, max_storage_size, max_app_disk_size, max_apps,
		    max_sites, max_site_deploy_bytes, max_site_files, max_site_file_bytes, max_site_deploys_per_hour, max_site_custom_domains,
		    site_build_cpu_millis, site_build_memory, site_build_disk, site_build_timeout_seconds, site_build_concurrency,
		    max_server_sites, max_site_server_bundle_bytes, updated_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $19, $20, $21, $22, $23, $24, $25, NOW())
		 ON CONFLICT (tier) DO UPDATE SET
		    max_projects          = EXCLUDED.max_projects,
		    instances             = EXCLUDED.instances,
		    storage_size          = EXCLUDED.storage_size,
		    max_storage_size      = EXCLUDED.max_storage_size,
		    max_app_disk_size     = EXCLUDED.max_app_disk_size,
		    max_apps              = EXCLUDED.max_apps,
		    memory                = EXCLUDED.memory,
		    cpu                   = EXCLUDED.cpu,
		    backup_enabled        = EXCLUDED.backup_enabled,
		    auto_pause_after_days = EXCLUDED.auto_pause_after_days,
		    max_sites             = EXCLUDED.max_sites,
		    max_site_deploy_bytes = EXCLUDED.max_site_deploy_bytes,
		    max_site_files        = EXCLUDED.max_site_files,
		    max_site_file_bytes   = EXCLUDED.max_site_file_bytes,
		    max_site_deploys_per_hour = EXCLUDED.max_site_deploys_per_hour,
		    max_site_custom_domains = EXCLUDED.max_site_custom_domains,
		    site_build_cpu_millis = EXCLUDED.site_build_cpu_millis,
		    site_build_memory     = EXCLUDED.site_build_memory,
		    site_build_disk       = EXCLUDED.site_build_disk,
		    site_build_timeout_seconds = EXCLUDED.site_build_timeout_seconds,
		    site_build_concurrency = EXCLUDED.site_build_concurrency,
		    max_server_sites      = EXCLUDED.max_server_sites,
		    max_site_server_bundle_bytes = EXCLUDED.max_site_server_bundle_bytes,
		    version               = tier_configs.version + 1,
		    updated_at            = NOW()
		 WHERE $18::INTEGER IS NULL OR tier_configs.version = $18
		 RETURNING version`,
		tier, tc.MaxProjects, tc.Instances, tc.StorageSize, tc.Memory, tc.CPU, tc.BackupEnabled, tc.AutoPauseAfterDays, tc.MaxStorageSize, tc.MaxAppDiskSize, tc.MaxApps,
		tc.Sites.MaxSites, tc.Sites.MaxDeployBytes, tc.Sites.MaxFiles, tc.Sites.MaxFileBytes, tc.Sites.MaxDeploysPerHour, tc.Sites.MaxCustomDomains, expectedVersion,
		tc.SiteBuilds.CPUMillis, tc.SiteBuilds.Memory, tc.SiteBuilds.Disk, tc.SiteBuilds.TimeoutSeconds, tc.SiteBuilds.Concurrency,
		tc.Sites.MaxServerSites, tc.Sites.MaxServerBundleBytes).Scan(&version)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, s.tierVersionConflict(ctx, tier)
	}
	return version, err
}

// tierVersionConflict names the version the stale edit lost to.
func (s *Store) tierVersionConflict(ctx context.Context, tier domain.TierType) error {
	var current int
	if err := s.db.QueryRowContext(ctx, `SELECT version FROM tier_configs WHERE tier = $1`, tier).Scan(&current); err != nil {
		return fmt.Errorf("%w; reading its version: %v", storage.ErrTierVersionConflict, err)
	}
	return fmt.Errorf("%w: it is now at version %d", storage.ErrTierVersionConflict, current)
}

type tierRowScanner interface{ Scan(dest ...any) error }

func scanTierConfig(r tierRowScanner) (domain.TierType, config.TierConfig, error) {
	var tier domain.TierType
	var tc config.TierConfig
	err := r.Scan(&tier, &tc.MaxProjects, &tc.Instances, &tc.StorageSize, &tc.MaxStorageSize, &tc.MaxAppDiskSize, &tc.MaxApps, &tc.Memory, &tc.CPU, &tc.BackupEnabled, &tc.AutoPauseAfterDays,
		&tc.Sites.MaxSites, &tc.Sites.MaxDeployBytes, &tc.Sites.MaxFiles, &tc.Sites.MaxFileBytes, &tc.Sites.MaxDeploysPerHour, &tc.Sites.MaxCustomDomains,
		&tc.SiteBuilds.CPUMillis, &tc.SiteBuilds.Memory, &tc.SiteBuilds.Disk, &tc.SiteBuilds.TimeoutSeconds, &tc.SiteBuilds.Concurrency, &tc.Version,
		&tc.Sites.MaxServerSites, &tc.Sites.MaxServerBundleBytes)
	return tier, tc, err
}
