CREATE TABLE IF NOT EXISTS backup_schedules (
    project_id     TEXT PRIMARY KEY,
    cron_spec      TEXT NOT NULL,
    retention_days INTEGER NOT NULL DEFAULT 7,
    enabled        INTEGER NOT NULL DEFAULT 1,
    created_at     TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at     TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_backup_schedules_enabled ON backup_schedules(enabled);
