-- The admin audit log (EXC-856) filters on this column and pages by id.
-- CONCURRENTLY: a plain build blocks audit inserts, and it needs a file of its own.
CREATE INDEX CONCURRENTLY idx_audit_log_resource_id_id ON audit_log (resource_id, id);
