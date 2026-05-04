-- See postgres mirror. SQLite ALTER TABLE ADD COLUMN allows DEFAULT
-- on NOT NULL columns; existing rows backfill to 'k8s'.
ALTER TABLE database_instances ADD COLUMN deployment_mode TEXT NOT NULL DEFAULT 'k8s';
