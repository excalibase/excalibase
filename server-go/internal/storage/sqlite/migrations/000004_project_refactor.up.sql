-- Project ref refactor: project_name separated from project_id,
-- plus rollback metadata columns added during rollback feature.
ALTER TABLE database_instances ADD COLUMN project_name TEXT DEFAULT '';
ALTER TABLE database_instances ADD COLUMN current_step TEXT DEFAULT '';
ALTER TABLE database_instances ADD COLUMN failure_stage TEXT DEFAULT '';
ALTER TABLE database_instances ADD COLUMN failure_step TEXT DEFAULT '';
ALTER TABLE database_instances ADD COLUMN rollback_log TEXT DEFAULT '';
