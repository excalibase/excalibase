-- Custom hostnames of sites (EXC-614): a customer's own domain, added to the
-- user-content zone through Cloudflare for SaaS. A project claims a hostname
-- once; across projects only the row whose owner proved control (verified_at)
-- holds it, so an unproven claim cannot squat a name. No ON DELETE CASCADE: a site is deleted only after each of
-- its hostnames left the zone, so none is left there untracked.
CREATE TABLE IF NOT EXISTS site_hostnames (
    id                 TEXT PRIMARY KEY,
    site_id            TEXT NOT NULL REFERENCES sites(id),
    project_id         TEXT NOT NULL,
    hostname           TEXT NOT NULL,
    validation         TEXT NOT NULL CHECK (validation IN ('http', 'txt')),
    status             TEXT NOT NULL CHECK (status IN ('PENDING', 'ACTIVE', 'FAILED')),
    -- Cloudflare's id; NULL until the create was answered.
    cloudflare_id      TEXT,
    hostname_status    TEXT NOT NULL DEFAULT '',
    certificate_status TEXT NOT NULL DEFAULT '',
    -- The DNS records the user adds, and what Cloudflare last said was wrong.
    records            JSONB NOT NULL DEFAULT '[]',
    errors             JSONB NOT NULL DEFAULT '[]',
    created_by         TEXT NOT NULL,
    created_at         TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at         TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    checked_at         TIMESTAMPTZ NOT NULL DEFAULT 'epoch',
    -- The owner proves control with a TXT record holding this token; a CNAME
    -- an earlier owner left behind would otherwise validate on its own.
    verification_token TEXT NOT NULL,
    verified_at        TIMESTAMPTZ,
    -- When it was added or stopped serving; NULL while active. Dropped after a week.
    inactive_since     TIMESTAMPTZ,
    -- When it last left the zone; it is not proven again for a cooldown after.
    withdrawn_at       TIMESTAMPTZ,
    CONSTRAINT site_hostnames_project_hostname_key UNIQUE (project_id, hostname)
);

CREATE UNIQUE INDEX IF NOT EXISTS site_hostnames_proven_hostname_key ON site_hostnames(hostname) WHERE verified_at IS NOT NULL;

CREATE INDEX IF NOT EXISTS idx_site_hostnames_site ON site_hostnames(project_id, site_id);
CREATE INDEX IF NOT EXISTS idx_site_hostnames_checked ON site_hostnames(checked_at);

-- Custom domains per project follow the plan with the other Sites limits
-- (EXC-620). Cloudflare includes 100 per zone and bills each one after.
ALTER TABLE tier_configs ADD COLUMN max_site_custom_domains INTEGER;
UPDATE tier_configs SET max_site_custom_domains = 2 WHERE tier = 'FREE';
UPDATE tier_configs SET max_site_custom_domains = 5 WHERE tier = 'STANDARD';
UPDATE tier_configs SET max_site_custom_domains = 10 WHERE tier = 'ENTERPRISE';
UPDATE tier_configs SET max_site_custom_domains = 2 WHERE max_site_custom_domains IS NULL;
ALTER TABLE tier_configs ALTER COLUMN max_site_custom_domains SET NOT NULL;
