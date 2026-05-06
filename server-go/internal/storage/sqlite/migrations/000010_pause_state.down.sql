DROP INDEX IF EXISTS idx_database_instances_status_tier;
ALTER TABLE database_instances DROP COLUMN last_active_at;
ALTER TABLE database_instances DROP COLUMN last_xact_count;
ALTER TABLE database_instances DROP COLUMN pause_reason;
