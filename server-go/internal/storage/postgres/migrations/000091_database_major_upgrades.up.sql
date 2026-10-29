-- Major version upgrades (EXC-668): one row per run, at most one running per
-- project. converted marks the run after which the data lives on to_version;
-- the converted rows also say which backup archive each major writes to.
-- Additive and unread while majorupgrade is off.
CREATE TABLE IF NOT EXISTS database_major_upgrades (
    id               TEXT PRIMARY KEY,
    project_id       TEXT NOT NULL REFERENCES database_instances(project_id) ON DELETE CASCADE,
    engine           TEXT NOT NULL,
    from_version     TEXT NOT NULL,
    to_version       TEXT NOT NULL,
    status           TEXT NOT NULL,
    step             TEXT NOT NULL DEFAULT '',
    safety_backup_id TEXT NOT NULL DEFAULT '',
    converted        BOOLEAN NOT NULL DEFAULT false,
    error            TEXT NOT NULL DEFAULT '',
    warnings         TEXT[] NOT NULL DEFAULT '{}',
    requested_by     TEXT NOT NULL DEFAULT '',
    started_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    writes_stopped_at TIMESTAMPTZ,
    converted_at     TIMESTAMPTZ,
    finished_at      TIMESTAMPTZ,
    -- The driver's last sign of life; recovery takes only a stale run.
    heartbeat_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT database_major_upgrades_status_known
        CHECK (status IN ('RUNNING', 'COMPLETED', 'ROLLED_BACK', 'FAILED')),
    CONSTRAINT database_major_upgrades_converted_when
        CHECK (converted = (converted_at IS NOT NULL))
);

CREATE UNIQUE INDEX IF NOT EXISTS database_major_upgrades_one_running
    ON database_major_upgrades (project_id) WHERE status = 'RUNNING';
CREATE INDEX IF NOT EXISTS database_major_upgrades_by_project
    ON database_major_upgrades (project_id, started_at DESC);

-- A single host's base backup records the major it was taken on, so a restore
-- of it runs that major and reads its archive.
ALTER TABLE backup_records ADD COLUMN IF NOT EXISTS postgres_version TEXT NOT NULL DEFAULT '';
