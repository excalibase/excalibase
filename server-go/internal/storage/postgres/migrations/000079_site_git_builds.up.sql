-- Git builds of sites (EXC-354, ADR 0043 decision 8). Numbered past the Sites
-- migrations other branches add, so it rebases without a renumber.

-- A plan's build limits, edited by an admin with the rest of the plan. The
-- starting numbers are the gVisor spike's.
ALTER TABLE tier_configs ADD COLUMN site_build_cpu_millis INTEGER NOT NULL DEFAULT 2000;
ALTER TABLE tier_configs ADD COLUMN site_build_memory TEXT NOT NULL DEFAULT '2Gi';
ALTER TABLE tier_configs ADD COLUMN site_build_disk TEXT NOT NULL DEFAULT '5Gi';
ALTER TABLE tier_configs ADD COLUMN site_build_timeout_seconds INTEGER NOT NULL DEFAULT 600;
ALTER TABLE tier_configs ADD COLUMN site_build_concurrency INTEGER NOT NULL DEFAULT 1;
UPDATE tier_configs SET site_build_memory = '4Gi', site_build_disk = '10Gi', site_build_timeout_seconds = 1200,
    site_build_concurrency = 2 WHERE tier IN ('STANDARD', 'ENTERPRISE');

-- One repository and branch per site, and how to build it.
CREATE TABLE IF NOT EXISTS site_git_connections (
    site_id         TEXT PRIMARY KEY REFERENCES sites(id) ON DELETE CASCADE,
    project_id      TEXT NOT NULL,
    provider        TEXT NOT NULL CHECK (provider IN ('github')),
    installation_id BIGINT NOT NULL,
    repository_id   BIGINT NOT NULL,
    -- owner/name as last read; a rename is followed through repository_id.
    repository      TEXT NOT NULL,
    branch          TEXT NOT NULL,
    root_dir        TEXT NOT NULL DEFAULT '',
    install_command TEXT NOT NULL DEFAULT '',
    build_command   TEXT NOT NULL DEFAULT '',
    output_dir      TEXT NOT NULL,
    node_version    TEXT NOT NULL,
    -- Plain, non-secret build variables; they end up in the built files.
    build_env       JSONB NOT NULL DEFAULT '{}',
    connected_by    TEXT NOT NULL,
    -- Set once the connector lost the developer rung; nothing builds until a developer reconnects.
    inactive_reason TEXT,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_site_git_connections_repo ON site_git_connections(provider, repository_id);
CREATE INDEX IF NOT EXISTS idx_site_git_connections_installation ON site_git_connections(provider, installation_id);

-- The build queue: QUEUED → RUNNING → SUCCEEDED / FAILED / CANCELLED. The
-- dispatcher claims under an advisory lock, so slots hold across replicas.
CREATE TABLE IF NOT EXISTS site_builds (
    id                  TEXT PRIMARY KEY,
    site_id             TEXT NOT NULL REFERENCES sites(id) ON DELETE CASCADE,
    project_id          TEXT NOT NULL,
    status              TEXT NOT NULL CHECK (status IN ('QUEUED', 'RUNNING', 'SUCCEEDED', 'FAILED', 'CANCELLED')),
    trigger             TEXT NOT NULL CHECK (trigger IN ('push', 'rebuild')),
    commit_sha          TEXT NOT NULL,
    commit_message      TEXT,
    -- The connection's repository and build settings when the build was queued.
    settings            JSONB NOT NULL,
    delivery_id         TEXT,
    -- The plan's limits when the build was queued.
    cpu_millis          INTEGER NOT NULL,
    memory              TEXT NOT NULL,
    disk                TEXT NOT NULL,
    timeout_seconds     INTEGER NOT NULL,
    project_concurrency INTEGER NOT NULL,
    deploy_id           TEXT,
    failure_kind        TEXT,
    failure_reason      TEXT,
    log                 TEXT,
    requested_by        TEXT NOT NULL,
    -- The replica running it and until when; a lapsed lease is a lost build.
    lease_holder        TEXT,
    lease_until         TIMESTAMPTZ,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    started_at          TIMESTAMPTZ,
    finished_at         TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS idx_site_builds_site ON site_builds(site_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_site_builds_queued ON site_builds(created_at) WHERE status = 'QUEUED';
CREATE INDEX IF NOT EXISTS idx_site_builds_project_status ON site_builds(project_id, status);
-- One running build per site; a redelivered push queues nothing new.
CREATE UNIQUE INDEX IF NOT EXISTS idx_site_builds_one_running ON site_builds(site_id) WHERE status = 'RUNNING';
CREATE UNIQUE INDEX IF NOT EXISTS idx_site_builds_delivery ON site_builds(site_id, delivery_id) WHERE delivery_id IS NOT NULL;

-- Project secrets a build may read. Absent = not visible, the default.
CREATE TABLE IF NOT EXISTS site_build_visible_secrets (
    project_id TEXT NOT NULL,
    name       TEXT NOT NULL,
    marked_by  TEXT NOT NULL,
    marked_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (project_id, name)
);

-- A sign-in to the git host in flight: single use, short-lived, kept hashed.
CREATE TABLE IF NOT EXISTS site_git_states (
    state_hash TEXT PRIMARY KEY,
    user_id    TEXT NOT NULL,
    project_id TEXT NOT NULL,
    site_id    TEXT NOT NULL,
    -- The starting browser's key, hashed; the callback is honoured only with it.
    browser_hash TEXT NOT NULL,
    expires_at TIMESTAMPTZ NOT NULL
);

-- Webhook deliveries handled in the last week; a replay under a seen id is ignored.
CREATE TABLE IF NOT EXISTS site_git_deliveries (
    provider    TEXT NOT NULL,
    delivery_id TEXT NOT NULL,
    received_at TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (provider, delivery_id)
);
CREATE INDEX IF NOT EXISTS idx_site_git_deliveries_received ON site_git_deliveries (received_at);

-- What a user's own git-host sign-in showed they may read; a connect needs a fresh row.
CREATE TABLE IF NOT EXISTS site_git_grants (
    user_id         TEXT NOT NULL,
    provider        TEXT NOT NULL,
    installation_id BIGINT NOT NULL,
    account         TEXT NOT NULL,
    repository_id   BIGINT NOT NULL,
    repository      TEXT NOT NULL,
    default_branch  TEXT NOT NULL,
    private         BOOLEAN NOT NULL,
    verified_at     TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (user_id, provider, installation_id, repository_id)
);
