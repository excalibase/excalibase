DROP TABLE IF EXISTS retained_backups;
ALTER TABLE database_instances DROP COLUMN IF EXISTS deletion_due_at;
ALTER TABLE database_instances DROP COLUMN IF EXISTS deletion_scheduled_at;
