ALTER TABLE project_activity DROP COLUMN IF EXISTS idle_warned_at;
ALTER TABLE tier_configs DROP COLUMN IF EXISTS auto_pause_after_days;
