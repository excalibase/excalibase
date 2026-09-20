ALTER TABLE database_instances DROP COLUMN IF EXISTS pause_backup_at;
ALTER TABLE database_instances DROP COLUMN IF EXISTS pause_backup_id;
ALTER TABLE database_instances DROP COLUMN IF EXISTS pause_last_attempt_at;
ALTER TABLE database_instances DROP COLUMN IF EXISTS pause_attempts;
