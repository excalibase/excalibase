ALTER TABLE database_instances DROP COLUMN IF EXISTS rollback_log;
ALTER TABLE database_instances DROP COLUMN IF EXISTS failure_step;
ALTER TABLE database_instances DROP COLUMN IF EXISTS failure_stage;
ALTER TABLE database_instances DROP COLUMN IF EXISTS current_step;
ALTER TABLE database_instances DROP COLUMN IF EXISTS project_name;
