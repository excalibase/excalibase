DROP INDEX IF EXISTS idx_database_instances_status_tier;
ALTER TABLE database_instances
    DROP COLUMN IF EXISTS last_active_at,
    DROP COLUMN IF EXISTS last_xact_count,
    DROP COLUMN IF EXISTS pause_reason;
