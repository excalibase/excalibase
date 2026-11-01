-- End-user auth hooks (EXC-662). Additive and unread while authhooks is off.
-- database_instances is the project row (a project without a database has
-- one too, EXC-426). The signing secret lives only in the vault at
-- projects/{project_id}/auth/hooks; a row here records that it is set.
CREATE TABLE IF NOT EXISTS project_auth_hooks (
    project_id TEXT NOT NULL REFERENCES database_instances(project_id) ON DELETE CASCADE,
    hook       TEXT NOT NULL CHECK (hook IN ('custom_access_token', 'before_user_created', 'send_email')),
    enabled    BOOLEAN NOT NULL DEFAULT false,
    url        TEXT NOT NULL DEFAULT '',
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (project_id, hook),
    CHECK (NOT enabled OR url <> '')
);

CREATE TABLE IF NOT EXISTS project_auth_hook_secrets (
    project_id TEXT PRIMARY KEY REFERENCES database_instances(project_id) ON DELETE CASCADE,
    rotated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
