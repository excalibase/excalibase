-- Tier configuration moved out of hardcoded Go (config/tiers.go) into the DB so
-- platform admins can tune CPU / RAM / storage (and replicas, for GA HA) per
-- tier from the admin UI without a redeploy. Seeded with the historical
-- defaults; the Go map stays the fallback when a tier row is absent.
CREATE TABLE IF NOT EXISTS tier_configs (
    tier           TEXT PRIMARY KEY,                 -- FREE / STANDARD / ENTERPRISE
    max_projects   INTEGER NOT NULL DEFAULT 0,       -- 0 = unlimited
    instances      INTEGER NOT NULL DEFAULT 1,       -- CNPG replicas (1 = no HA)
    storage_size   TEXT NOT NULL,
    memory         TEXT NOT NULL,
    cpu            TEXT NOT NULL,
    backup_enabled BOOLEAN NOT NULL DEFAULT FALSE,
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

INSERT INTO tier_configs (tier, max_projects, instances, storage_size, memory, cpu, backup_enabled) VALUES
    ('FREE',       1, 1, '5Gi',   '512Mi', '0.5', FALSE),
    ('STANDARD',   5, 1, '50Gi',  '4Gi',   '2',   TRUE),
    ('ENTERPRISE', 0, 1, '500Gi', '16Gi',  '4',   TRUE)
ON CONFLICT (tier) DO NOTHING;
