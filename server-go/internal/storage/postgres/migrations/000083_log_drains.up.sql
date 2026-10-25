-- Log drains (EXC-658): where a project's logs are forwarded and how far.
-- The key, header value and full endpoint URL live in the vault; url here is
-- scheme and host only. lease_* is the batch handed to the forwarder, the
-- only one an ack may settle. Additive: unused while logdrains is off.
CREATE TABLE IF NOT EXISTS log_drains (
    id TEXT PRIMARY KEY,
    project_id TEXT NOT NULL,
    name TEXT NOT NULL,
    kind TEXT NOT NULL CHECK (kind IN ('datadog', 'https')),
    datadog_site TEXT NOT NULL DEFAULT '',
    url TEXT NOT NULL DEFAULT '',
    header_name TEXT NOT NULL DEFAULT '',
    enabled BOOLEAN NOT NULL DEFAULT TRUE,
    status TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'failing', 'disabled')),
    last_error TEXT NOT NULL DEFAULT '',
    consecutive_failures INT NOT NULL DEFAULT 0,
    cursor_ns BIGINT NOT NULL,
    lease_to BIGINT NOT NULL DEFAULT 0,
    lease_lines INT NOT NULL DEFAULT 0,
    next_attempt_at TIMESTAMPTZ NOT NULL,
    last_success_at TIMESTAMPTZ,
    sent_lines BIGINT NOT NULL DEFAULT 0,
    dropped_lines BIGINT NOT NULL DEFAULT 0,
    created_by TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_log_drains_project ON log_drains(project_id);
CREATE INDEX IF NOT EXISTS idx_log_drains_due ON log_drains(next_attempt_at) WHERE enabled;
