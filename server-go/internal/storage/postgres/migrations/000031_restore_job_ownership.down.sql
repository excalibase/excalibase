DROP INDEX IF EXISTS idx_restore_jobs_heartbeat;
ALTER TABLE restore_jobs DROP COLUMN IF EXISTS heartbeat_at;
ALTER TABLE restore_jobs DROP COLUMN IF EXISTS owner;
