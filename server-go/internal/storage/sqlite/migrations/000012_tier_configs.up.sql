-- SQLite mirror of the Postgres tier_configs table. BOOLEAN → INTEGER (0/1).
CREATE TABLE IF NOT EXISTS tier_configs (
    tier           TEXT PRIMARY KEY,
    max_projects   INTEGER NOT NULL DEFAULT 0,
    instances      INTEGER NOT NULL DEFAULT 1,
    storage_size   TEXT NOT NULL,
    memory         TEXT NOT NULL,
    cpu            TEXT NOT NULL,
    backup_enabled INTEGER NOT NULL DEFAULT 0,
    updated_at     TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

INSERT OR IGNORE INTO tier_configs (tier, max_projects, instances, storage_size, memory, cpu, backup_enabled) VALUES
    ('FREE',       1, 1, '5Gi',   '512Mi', '0.5', 0),
    ('STANDARD',   5, 1, '50Gi',  '4Gi',   '2',   1),
    ('ENTERPRISE', 0, 1, '500Gi', '16Gi',  '4',   1);
