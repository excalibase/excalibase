-- The admin audit log (EXC-856) filters by actor, target and action prefix and
-- pages by id. Without these a filter that matches nothing walks the whole table.
CREATE INDEX IF NOT EXISTS idx_audit_log_user_id_id ON audit_log (user_id, id);
CREATE INDEX IF NOT EXISTS idx_audit_log_resource_id_id ON audit_log (resource_id, id);
CREATE INDEX IF NOT EXISTS idx_audit_log_project_id_id ON audit_log (project_id, id) WHERE project_id <> '';
CREATE INDEX IF NOT EXISTS idx_audit_log_action_prefix ON audit_log (action text_pattern_ops, id);
