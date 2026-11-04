-- Git builds of apps share the site build queue (EXC-547 step 4): one table,
-- so the dispatcher's claim counts running builds of both kinds against the
-- install's slots. Tables keep their site_ names; other open work touches them.

ALTER TABLE site_builds ADD COLUMN kind TEXT NOT NULL DEFAULT 'site';
ALTER TABLE site_builds ADD CONSTRAINT site_builds_kind_known CHECK (kind IN ('site', 'app'));
ALTER TABLE site_builds ADD COLUMN app_id TEXT;
-- App ids are unique per project only, so the link carries the project.
ALTER TABLE site_builds ADD CONSTRAINT site_builds_app_fk FOREIGN KEY (project_id, app_id)
    REFERENCES apps(project_id, id) ON DELETE CASCADE;
ALTER TABLE site_builds ALTER COLUMN site_id DROP NOT NULL;
ALTER TABLE site_builds ADD CONSTRAINT site_builds_one_target CHECK (
    (kind = 'site' AND site_id IS NOT NULL AND app_id IS NULL) OR
    (kind = 'app' AND app_id IS NOT NULL AND site_id IS NULL));

CREATE INDEX IF NOT EXISTS idx_site_builds_app ON site_builds(project_id, app_id, created_at DESC) WHERE app_id IS NOT NULL;
-- One running build per app; a redelivered push queues nothing new.
CREATE UNIQUE INDEX IF NOT EXISTS idx_site_builds_app_one_running ON site_builds(project_id, app_id)
    WHERE status = 'RUNNING' AND app_id IS NOT NULL;
CREATE UNIQUE INDEX IF NOT EXISTS idx_site_builds_app_delivery ON site_builds(project_id, app_id, delivery_id)
    WHERE delivery_id IS NOT NULL AND app_id IS NOT NULL;

-- One repository and branch per app, and how to build its image.
CREATE TABLE IF NOT EXISTS app_git_connections (
    project_id      TEXT NOT NULL,
    app_id          TEXT NOT NULL,
    provider        TEXT NOT NULL CHECK (provider IN ('github')),
    installation_id BIGINT NOT NULL,
    repository_id   BIGINT NOT NULL,
    -- owner/name as last read; a rename is followed through repository_id.
    repository      TEXT NOT NULL,
    branch          TEXT NOT NULL,
    -- auto picks dockerfile when the commit has one, buildpacks otherwise.
    builder         TEXT NOT NULL DEFAULT 'auto' CHECK (builder IN ('auto', 'dockerfile', 'buildpacks')),
    root_dir        TEXT NOT NULL DEFAULT '',
    dockerfile_path TEXT NOT NULL DEFAULT '',
    -- Plain, non-secret build arguments; they end up in the image's history.
    build_args      JSONB NOT NULL DEFAULT '{}',
    connected_by    TEXT NOT NULL,
    -- Set once the connector lost the developer rung; nothing builds until a developer reconnects.
    inactive_reason TEXT,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (project_id, app_id),
    FOREIGN KEY (project_id, app_id) REFERENCES apps(project_id, id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_app_git_connections_repo ON app_git_connections(provider, repository_id);
CREATE INDEX IF NOT EXISTS idx_app_git_connections_installation ON app_git_connections(provider, installation_id);

-- A plan's app builds per project per hour; it starts at the plan's site deploys per hour.
ALTER TABLE tier_configs ADD COLUMN app_build_max_per_hour INTEGER NOT NULL DEFAULT 30;
UPDATE tier_configs SET app_build_max_per_hour = max_site_deploys_per_hour
    WHERE app_build_max_per_hour IS DISTINCT FROM max_site_deploys_per_hour;

-- Each app build queued in the last hour, counted against the plan's app builds per hour.
CREATE TABLE IF NOT EXISTS app_build_attempts (
    project_id   TEXT NOT NULL,
    attempted_at TIMESTAMPTZ NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_app_build_attempts_project ON app_build_attempts (project_id, attempted_at);

-- Builds of both kinds read this one project-wide list.
COMMENT ON TABLE site_build_visible_secrets IS 'Project secrets every git build of the project reads, site and app alike';
