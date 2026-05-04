-- Track per-instance deployment mode so the BackupAdapter (and any
-- future per-mode dispatch) can route correctly. Existing rows
-- backfill to 'k8s' since CNPG was the only mode that wrote to this
-- table before the Docker mode landed.
ALTER TABLE database_instances
    ADD COLUMN IF NOT EXISTS deployment_mode TEXT NOT NULL DEFAULT 'k8s';
