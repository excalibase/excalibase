-- Restore provenance (EXC-366). A restored project is registered exactly like
-- a provisioned one; these two columns are the only thing that distinguishes
-- it, so support can answer "where did this data come from" without joining
-- restore_jobs (which is purged on retention).
ALTER TABLE database_instances
    ADD COLUMN IF NOT EXISTS restored_from_project_id TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS restored_from_backup_id TEXT NOT NULL DEFAULT '';
