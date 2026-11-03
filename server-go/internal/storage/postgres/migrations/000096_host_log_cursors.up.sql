-- Single-host stored logs (EXC-672): per container, the newest line the
-- bundled Loki has stored, so a restart resumes without gaps or repeats.
CREATE TABLE IF NOT EXISTS host_log_cursors (
    container_id TEXT PRIMARY KEY,
    project_id TEXT NOT NULL,
    shipped_until_ns BIGINT NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
