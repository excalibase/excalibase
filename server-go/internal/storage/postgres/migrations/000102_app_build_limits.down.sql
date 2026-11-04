ALTER TABLE tier_configs DROP COLUMN IF EXISTS app_build_max_image;
ALTER TABLE tier_configs DROP COLUMN IF EXISTS app_build_concurrency;
ALTER TABLE tier_configs DROP COLUMN IF EXISTS app_build_timeout_seconds;
ALTER TABLE tier_configs DROP COLUMN IF EXISTS app_build_disk;
ALTER TABLE tier_configs DROP COLUMN IF EXISTS app_build_memory;
ALTER TABLE tier_configs DROP COLUMN IF EXISTS app_build_cpu_millis;
