DROP INDEX IF EXISTS idx_audit_log_project_via;
ALTER TABLE audit_log DROP COLUMN IF EXISTS via;
ALTER TABLE audit_log DROP COLUMN IF EXISTS token_hash;
ALTER TABLE audit_log DROP COLUMN IF EXISTS project_id;
