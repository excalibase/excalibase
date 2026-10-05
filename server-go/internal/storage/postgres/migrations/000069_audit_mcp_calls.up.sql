-- Calls through the hosted MCP endpoint (EXC-544) are audited per project and
-- per personal access token, so Studio can list what an AI tool did.
ALTER TABLE audit_log ADD COLUMN IF NOT EXISTS project_id TEXT NOT NULL DEFAULT '';
ALTER TABLE audit_log ADD COLUMN IF NOT EXISTS token_hash TEXT NOT NULL DEFAULT '';
ALTER TABLE audit_log ADD COLUMN IF NOT EXISTS via TEXT NOT NULL DEFAULT '';

CREATE INDEX IF NOT EXISTS idx_audit_log_project_via ON audit_log (project_id, via, timestamp DESC) WHERE via <> '';
