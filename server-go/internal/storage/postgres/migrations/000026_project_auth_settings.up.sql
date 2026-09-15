-- Per-project auth settings (EXC-367).
--
-- The auth service reads this through GET /api/projects/{id}/info to decide
-- whether a new signup must verify its email before it can log in, and to
-- build the correct redirect/callback URL for that project. No row = the
-- zero value: verification off, no site URL configured.
CREATE TABLE IF NOT EXISTS project_auth_settings (
    project_id                 TEXT PRIMARY KEY REFERENCES database_instances(project_id) ON DELETE CASCADE,
    require_email_verification BOOLEAN NOT NULL DEFAULT false,
    site_url                   TEXT NOT NULL DEFAULT '',
    updated_at                 TIMESTAMPTZ NOT NULL DEFAULT now()
);
