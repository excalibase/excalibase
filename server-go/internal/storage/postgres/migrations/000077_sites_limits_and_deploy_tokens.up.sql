-- Sites limits per plan (EXC-620, ADR 0043 decision 11), edited by an admin
-- with the rest of the plan. FREE keeps the numbers Sites shipped with.
ALTER TABLE tier_configs ADD COLUMN max_sites INTEGER;
ALTER TABLE tier_configs ADD COLUMN max_site_deploy_bytes BIGINT;
ALTER TABLE tier_configs ADD COLUMN max_site_files INTEGER;
ALTER TABLE tier_configs ADD COLUMN max_site_file_bytes BIGINT;
ALTER TABLE tier_configs ADD COLUMN max_site_deploys_per_hour INTEGER;
UPDATE tier_configs SET max_sites = 5, max_site_deploys_per_hour = 30 WHERE tier = 'FREE';
UPDATE tier_configs SET max_sites = 20, max_site_deploys_per_hour = 120 WHERE tier = 'STANDARD';
UPDATE tier_configs SET max_sites = 50, max_site_deploys_per_hour = 300 WHERE tier = 'ENTERPRISE';
UPDATE tier_configs SET max_sites = 0 WHERE max_sites IS NULL;
UPDATE tier_configs SET max_site_deploys_per_hour = 30 WHERE max_site_deploys_per_hour IS NULL;
UPDATE tier_configs SET max_site_deploy_bytes = 104857600, max_site_files = 20000, max_site_file_bytes = 26214400
    WHERE max_site_deploy_bytes IS NULL;
ALTER TABLE tier_configs ALTER COLUMN max_sites SET NOT NULL;
ALTER TABLE tier_configs ALTER COLUMN max_site_deploy_bytes SET NOT NULL;
ALTER TABLE tier_configs ALTER COLUMN max_site_files SET NOT NULL;
ALTER TABLE tier_configs ALTER COLUMN max_site_file_bytes SET NOT NULL;
ALTER TABLE tier_configs ALTER COLUMN max_site_deploys_per_hour SET NOT NULL;
-- Each edit bumps it; an admin's save made from an older version is refused.
ALTER TABLE tier_configs ADD COLUMN version INTEGER NOT NULL DEFAULT 1;

-- Every deploy upload a project starts, refused or not, for its plan's deploys
-- per hour. Rows older than the hour are deleted on every attempt, any project's.
CREATE TABLE IF NOT EXISTS site_deploy_attempts (
    project_id   TEXT NOT NULL,
    attempted_at TIMESTAMPTZ NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_site_deploy_attempts_project ON site_deploy_attempts(project_id, attempted_at DESC);
CREATE INDEX IF NOT EXISTS idx_site_deploy_attempts_at ON site_deploy_attempts(attempted_at);

-- A deploy token is scoped to one site; only its SHA-256 is kept, as for a
-- personal access token. Deleting the site (or its project's sites) deletes it.
CREATE TABLE IF NOT EXISTS site_deploy_tokens (
    id           TEXT PRIMARY KEY,
    site_id      TEXT NOT NULL REFERENCES sites(id) ON DELETE CASCADE,
    project_id   TEXT NOT NULL,
    name         TEXT NOT NULL,
    token_hash   TEXT NOT NULL UNIQUE,
    token_prefix TEXT NOT NULL,
    created_by   TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    -- At most a year, and never past the personal access token that made it.
    expires_at   TIMESTAMPTZ NOT NULL,
    last_used_at TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS idx_site_deploy_tokens_site ON site_deploy_tokens(site_id);

-- The deploy token a deploy was made with, kept after the token is revoked.
ALTER TABLE site_deploys ADD COLUMN deploy_token_id TEXT;
ALTER TABLE site_deploys ADD COLUMN deploy_token_name TEXT;
