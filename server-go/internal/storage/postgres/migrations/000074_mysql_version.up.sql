-- MySQL projects (ADR 0041) record the version they run, as Postgres projects
-- record their major.
ALTER TABLE database_instances ADD COLUMN IF NOT EXISTS mysql_version TEXT NOT NULL DEFAULT '';
