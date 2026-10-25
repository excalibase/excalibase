-- End-user OAuth sign-in (EXC-662). Additive and unused while authoauth is off:
-- the redirect allow-list and the new-user switch join the auth settings, and
-- each provider keeps its client id here; the client secret lives only in the
-- vault at projects/{project_id}/auth/providers/{provider}.
ALTER TABLE project_auth_settings
    ADD COLUMN IF NOT EXISTS redirect_urls TEXT[] NOT NULL DEFAULT '{}',
    ADD COLUMN IF NOT EXISTS allow_new_users BOOLEAN NOT NULL DEFAULT true;

-- Keyed off database_instances like project_auth_settings; the auth-as-add-on work may re-key it.
CREATE TABLE IF NOT EXISTS project_auth_providers (
    project_id        TEXT NOT NULL REFERENCES database_instances(project_id) ON DELETE CASCADE,
    provider          TEXT NOT NULL CHECK (provider IN ('google', 'github')),
    enabled           BOOLEAN NOT NULL DEFAULT false,
    client_id         TEXT NOT NULL DEFAULT '',
    client_secret_set BOOLEAN NOT NULL DEFAULT false,
    updated_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (project_id, provider)
);
