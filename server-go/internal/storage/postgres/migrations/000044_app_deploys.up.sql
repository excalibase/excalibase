-- One row per attempt to roll an app's current record out to the cluster.
-- `spec` snapshots env variable NAMES only, never values.
-- A deploy is always accepted; an earlier pending/rolling deploy is superseded.
CREATE TABLE IF NOT EXISTS app_deploys (
    id             TEXT PRIMARY KEY,
    app_id         TEXT NOT NULL,
    project_id     TEXT NOT NULL,
    revision       INTEGER NOT NULL,
    image          TEXT NOT NULL,
    spec           JSONB NOT NULL,
    status         TEXT NOT NULL,
    failure_reason TEXT,
    created_by     TEXT NOT NULL,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    finished_at    TIMESTAMPTZ,
    CONSTRAINT app_deploys_app_fk FOREIGN KEY (project_id, app_id)
        REFERENCES apps (project_id, id) ON DELETE CASCADE,
    CONSTRAINT app_deploys_app_revision_key UNIQUE (app_id, revision),
    CONSTRAINT app_deploys_status_known CHECK (status IN ('pending', 'rolling', 'succeeded', 'failed', 'superseded'))
);

CREATE INDEX IF NOT EXISTS idx_app_deploys_app_revision ON app_deploys (app_id, revision DESC);
