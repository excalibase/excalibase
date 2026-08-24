-- Persisted backup schedules. The platform-side scheduler replays
-- these into a robfig/cron runtime on Start so cron registrations
-- survive restarts. Leader election is via pg_advisory_lock; no
-- separate state required.
CREATE TABLE IF NOT EXISTS backup_schedules (
    project_id     TEXT PRIMARY KEY,
    cron_spec      TEXT NOT NULL,
    retention_days INTEGER NOT NULL DEFAULT 7,
    enabled        BOOLEAN NOT NULL DEFAULT TRUE,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_backup_schedules_enabled ON backup_schedules(enabled);
