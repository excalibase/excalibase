-- Last-seen marker per project, fed by throttled writes from the platform
-- API (Studio calls, data-plane policy fetches, function invocations). Read
-- by the idle-pause scheduler. Separate from database_instances so the hot
-- write path never contends with provisioning state; cascades away with the
-- project.
CREATE TABLE IF NOT EXISTS project_activity (
    project_id       TEXT PRIMARY KEY REFERENCES database_instances(project_id) ON DELETE CASCADE,
    last_seen_at     TIMESTAMPTZ NOT NULL,
    last_seen_source TEXT NOT NULL DEFAULT ''
);

CREATE INDEX IF NOT EXISTS idx_project_activity_last_seen ON project_activity(last_seen_at);
