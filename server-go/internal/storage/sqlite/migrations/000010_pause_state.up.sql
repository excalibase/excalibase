ALTER TABLE database_instances ADD COLUMN last_active_at TEXT;
ALTER TABLE database_instances ADD COLUMN last_xact_count INTEGER NOT NULL DEFAULT 0;
ALTER TABLE database_instances ADD COLUMN pause_reason TEXT NOT NULL DEFAULT '';

CREATE INDEX IF NOT EXISTS idx_database_instances_status_tier
    ON database_instances(status, tier);
