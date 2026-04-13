-- Project ref refactor: project_name separated from project_id,
-- plus rollback metadata columns added during rollback feature.
ALTER TABLE database_instances ADD COLUMN IF NOT EXISTS project_name TEXT DEFAULT '';
ALTER TABLE database_instances ADD COLUMN IF NOT EXISTS current_step TEXT DEFAULT '';
ALTER TABLE database_instances ADD COLUMN IF NOT EXISTS failure_stage TEXT DEFAULT '';
ALTER TABLE database_instances ADD COLUMN IF NOT EXISTS failure_step TEXT DEFAULT '';
ALTER TABLE database_instances ADD COLUMN IF NOT EXISTS rollback_log TEXT DEFAULT '';
