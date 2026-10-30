-- A deleted project's stored log lines (EXC-678): one row per project, queued
-- by the teardown and worked until Loki says it removed them.
CREATE TABLE IF NOT EXISTS project_log_deletions (
    project_id TEXT PRIMARY KEY,
    selector TEXT NOT NULL,
    start_at TIMESTAMPTZ NOT NULL,
    -- Shortly after the teardown: a project id used again later keeps its own lines.
    end_at TIMESTAMPTZ NOT NULL,
    state TEXT NOT NULL DEFAULT 'pending' CHECK (state IN ('pending', 'requested', 'done', 'refused', 'lost')),
    attempts INTEGER NOT NULL DEFAULT 0,
    resends INTEGER NOT NULL DEFAULT 0,
    next_attempt_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    lease_until TIMESTAMPTZ,
    requested_at TIMESTAMPTZ,
    done_at TIMESTAMPTZ,
    last_error TEXT NOT NULL DEFAULT '',
    queued_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS project_log_deletions_due
    ON project_log_deletions (next_attempt_at) WHERE state IN ('pending', 'requested');
