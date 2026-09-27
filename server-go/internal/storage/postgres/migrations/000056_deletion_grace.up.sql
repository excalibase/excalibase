-- A deleted project is kept stopped for a grace period before the hard delete,
-- and a deleted project's kept backups are purged after a retention period.
ALTER TABLE database_instances ADD COLUMN IF NOT EXISTS deletion_scheduled_at TIMESTAMPTZ;
ALTER TABLE database_instances ADD COLUMN IF NOT EXISTS deletion_due_at TIMESTAMPTZ;

CREATE TABLE IF NOT EXISTS retained_backups (
    project_id      TEXT PRIMARY KEY,
    org_id          TEXT NOT NULL,
    deployment_mode TEXT NOT NULL,
    deleted_at      TIMESTAMPTZ NOT NULL,
    purge_after     TIMESTAMPTZ NOT NULL
);
CREATE INDEX IF NOT EXISTS retained_backups_purge_after_idx ON retained_backups (purge_after);
