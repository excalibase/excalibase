-- Restore jobs are the async record of a restore-into-new-project
-- operation. Phase 3's orchestrator returns RESTORING immediately,
-- runs the multi-step pipeline asynchronously, and updates this row
-- as it progresses. Platform restart mid-flight is observable —
-- the row stays RUNNING until either a worker completes it or the
-- janitor marks it FAILED past the timeout.
CREATE TABLE IF NOT EXISTS restore_jobs (
    id                 TEXT PRIMARY KEY,
    source_project_id  TEXT NOT NULL,
    new_project_id     TEXT NOT NULL,
    status             TEXT NOT NULL DEFAULT 'RUNNING',
    current_step       TEXT NOT NULL DEFAULT '',
    target_kind        TEXT NOT NULL DEFAULT 'latest', -- latest | time | xid | lsn | name
    target_value       TEXT NOT NULL DEFAULT '',
    failure_reason     TEXT NOT NULL DEFAULT '',
    created_at         TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at         TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_restore_jobs_status ON restore_jobs(status);
CREATE INDEX IF NOT EXISTS idx_restore_jobs_source ON restore_jobs(source_project_id);
