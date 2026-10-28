-- Server-rendered sites (EXC-661, ADR 0045): a site is static or server for
-- its whole life, and each deploy records which, so a rollback publishes it
-- the same way. Existing sites and deploys are static.
ALTER TABLE sites ADD COLUMN kind TEXT NOT NULL DEFAULT 'static' CHECK (kind IN ('static', 'server'));
ALTER TABLE site_deploys ADD COLUMN kind TEXT NOT NULL DEFAULT 'static' CHECK (kind IN ('static', 'server'));
-- A server deploy's site.json (main module, compatibility date and flags) and code size.
ALTER TABLE site_deploys ADD COLUMN server_config JSONB;

-- A server deploy's modules (tar.gz), kept for the rollback window: the
-- newest succeeded deploys of the site and its live one. Cloudflare keeps
-- asset bodies by hash, but nothing else keeps code.
CREATE TABLE IF NOT EXISTS site_deploy_code (
    deploy_id  TEXT PRIMARY KEY REFERENCES site_deploys(id) ON DELETE CASCADE,
    site_id    TEXT NOT NULL REFERENCES sites(id) ON DELETE CASCADE,
    project_id TEXT NOT NULL,
    packed     BYTEA NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_site_deploy_code_site ON site_deploy_code(site_id);

-- Per plan: how many sites may be server-rendered, and their code size uncompressed.
ALTER TABLE tier_configs ADD COLUMN max_server_sites INTEGER;
ALTER TABLE tier_configs ADD COLUMN max_site_server_bundle_bytes BIGINT;
UPDATE tier_configs SET max_server_sites = 1, max_site_server_bundle_bytes = 10485760 WHERE tier = 'FREE';
UPDATE tier_configs SET max_server_sites = 3, max_site_server_bundle_bytes = 31457280 WHERE tier = 'STANDARD';
UPDATE tier_configs SET max_server_sites = 10, max_site_server_bundle_bytes = 31457280 WHERE tier = 'ENTERPRISE';
UPDATE tier_configs SET max_server_sites = 0 WHERE max_server_sites IS NULL;
UPDATE tier_configs SET max_site_server_bundle_bytes = 10485760 WHERE max_site_server_bundle_bytes IS NULL;
ALTER TABLE tier_configs ALTER COLUMN max_server_sites SET NOT NULL;
ALTER TABLE tier_configs ALTER COLUMN max_site_server_bundle_bytes SET NOT NULL;
