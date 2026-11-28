-- The admin audit log (EXC-856) filters on this column and pages by id.
-- CONCURRENTLY: a plain build blocks audit inserts, and it needs a file of its own.
CREATE INDEX CONCURRENTLY idx_audit_log_action_prefix ON audit_log (action text_pattern_ops, id);
