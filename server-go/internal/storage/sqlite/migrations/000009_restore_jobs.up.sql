CREATE TABLE IF NOT EXISTS restore_jobs (
    id                 TEXT PRIMARY KEY,
    source_project_id  TEXT NOT NULL,
    new_project_id     TEXT NOT NULL,
    status             TEXT NOT NULL DEFAULT 'RUNNING',
    current_step       TEXT NOT NULL DEFAULT '',
    target_kind        TEXT NOT NULL DEFAULT 'latest',
    target_value       TEXT NOT NULL DEFAULT '',
    failure_reason     TEXT NOT NULL DEFAULT '',
    created_at         TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at         TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_restore_jobs_status ON restore_jobs(status);
CREATE INDEX IF NOT EXISTS idx_restore_jobs_source ON restore_jobs(source_project_id);
