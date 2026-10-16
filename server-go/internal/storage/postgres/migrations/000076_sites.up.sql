-- Sites (EXC-353): a project's built frontend, served from a user Worker in the
-- platform's Workers for Platforms namespace. The name is the hostname's first
-- label, so it is unique across every project, not only within one.
CREATE TABLE IF NOT EXISTS sites (
    id              TEXT PRIMARY KEY,
    project_id      TEXT NOT NULL,
    name            TEXT NOT NULL,
    hostname        TEXT NOT NULL,
    spa_fallback    BOOLEAN NOT NULL DEFAULT TRUE,
    live_deploy_id  TEXT,
    -- Set once the site's origin was offered to the project's CORS list.
    cors_claimed    BOOLEAN NOT NULL DEFAULT FALSE,
    -- The lease one deploy, rollback or deletion holds; NULL or past is free.
    -- deploy_lease is the holder's token, so a stale holder cannot write over the next one.
    deploying_until TIMESTAMPTZ,
    deploy_lease    TEXT,
    created_by      TEXT NOT NULL,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT sites_name_key UNIQUE (name),
    CONSTRAINT sites_hostname_key UNIQUE (hostname)
);

CREATE INDEX IF NOT EXISTS idx_sites_project ON sites(project_id);

-- Deploys are immutable records of one upload; the manifest is what a rollback publishes again.
CREATE TABLE IF NOT EXISTS site_deploys (
    id             TEXT PRIMARY KEY,
    site_id        TEXT NOT NULL REFERENCES sites(id) ON DELETE CASCADE,
    project_id     TEXT NOT NULL,
    status         TEXT NOT NULL CHECK (status IN ('PENDING', 'SUCCEEDED', 'FAILED')),
    manifest       JSONB NOT NULL,
    file_count     INTEGER NOT NULL,
    total_bytes    BIGINT NOT NULL,
    spa_fallback   BOOLEAN NOT NULL,
    label          TEXT,
    failure_reason TEXT,
    created_by     TEXT NOT NULL,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    finished_at    TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS idx_site_deploys_site ON site_deploys(site_id, created_at DESC);
