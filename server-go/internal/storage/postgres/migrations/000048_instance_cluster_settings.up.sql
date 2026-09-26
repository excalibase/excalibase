-- The cluster settings a project was created with, so a restore can rebuild
-- the same cluster. Fixed at creation: no UPDATE writes them.
ALTER TABLE database_instances
    ADD COLUMN storage_class TEXT NOT NULL DEFAULT '',
    ADD COLUMN parameters JSONB NOT NULL DEFAULT '{}'::jsonb;
