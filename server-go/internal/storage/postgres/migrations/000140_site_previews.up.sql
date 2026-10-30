-- Branch and pull request previews of sites (EXC-660). Placeholder number; renumbered before merge.

-- Live previews one project may hold, edited with the rest of the plan; each is one Worker script.
ALTER TABLE tier_configs ADD COLUMN max_site_previews INTEGER NOT NULL DEFAULT 2;
UPDATE tier_configs SET max_site_previews = 10 WHERE tier = 'STANDARD';
UPDATE tier_configs SET max_site_previews = 25 WHERE tier = 'ENTERPRISE';

-- One preview per site and pull request or branch, served at its own unguessable hostname.
CREATE TABLE IF NOT EXISTS site_previews (
    id             TEXT PRIMARY KEY,
    site_id        TEXT NOT NULL REFERENCES sites(id) ON DELETE CASCADE,
    project_id     TEXT NOT NULL,
    source         TEXT NOT NULL CHECK (source IN ('pull-request', 'branch')),
    ref            TEXT NOT NULL,
    -- A pull request from another repository; turning forks off removes these.
    from_fork      BOOLEAN NOT NULL DEFAULT FALSE,
    hostname       TEXT NOT NULL UNIQUE,
    status         TEXT NOT NULL CHECK (status IN ('BUILDING', 'READY', 'FAILED', 'REMOVING')),
    commit_sha     TEXT NOT NULL DEFAULT '',
    deployed_sha   TEXT,
    failure_reason TEXT,
    file_count     INTEGER NOT NULL DEFAULT 0,
    total_bytes    BIGINT NOT NULL DEFAULT 0,
    -- Set once the preview's origin was added to the project's CORS list (an opt-in).
    cors_claimed   BOOLEAN NOT NULL DEFAULT FALSE,
    lease_token    TEXT,
    lease_until    TIMESTAMPTZ,
    created_by     TEXT NOT NULL DEFAULT '',
    created_at     TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    deployed_at    TIMESTAMPTZ,
    UNIQUE (site_id, source, ref)
);
CREATE INDEX IF NOT EXISTS idx_site_previews_project ON site_previews(project_id);
CREATE INDEX IF NOT EXISTS idx_site_previews_updated ON site_previews(updated_at);

-- Which pushes and pull requests get previews, and the opt-ins an Admin turned on.
ALTER TABLE site_git_connections ADD COLUMN preview_settings JSONB NOT NULL DEFAULT '{}';

-- A preview's builds go with it; supersede and requeue stay within one preview.
ALTER TABLE site_builds ADD COLUMN preview_id TEXT REFERENCES site_previews(id) ON DELETE CASCADE;
ALTER TABLE site_builds DROP CONSTRAINT IF EXISTS site_builds_trigger_check;
ALTER TABLE site_builds ADD CONSTRAINT site_builds_trigger_check CHECK (trigger IN ('push', 'rebuild', 'preview'));
CREATE INDEX IF NOT EXISTS idx_site_builds_preview ON site_builds(preview_id) WHERE preview_id IS NOT NULL;

-- One running build per site and scope: a preview builds beside the site's production build.
DROP INDEX IF EXISTS idx_site_builds_one_running;
CREATE UNIQUE INDEX IF NOT EXISTS idx_site_builds_one_running ON site_builds(site_id, COALESCE(preview_id, '')) WHERE status = 'RUNNING';
