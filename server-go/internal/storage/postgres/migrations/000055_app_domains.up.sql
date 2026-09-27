-- Custom domains of Containers apps. A domain only holds its hostname once
-- routed (issuing, active, issue_failed): pending claims never block the
-- owner, and the partial unique index makes the verified one win.
CREATE TABLE IF NOT EXISTS app_domains (
    id                   TEXT PRIMARY KEY,
    project_id           TEXT NOT NULL,
    app_id               TEXT NOT NULL,
    hostname             TEXT NOT NULL,
    status               TEXT NOT NULL,
    failure_reason       TEXT,
    consecutive_failures INTEGER NOT NULL DEFAULT 0,
    verified_at          TIMESTAMPTZ,
    last_checked_at      TIMESTAMPTZ,
    created_at           TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT app_domains_app_fk FOREIGN KEY (project_id, app_id)
        REFERENCES apps (project_id, id) ON DELETE CASCADE,
    CONSTRAINT app_domains_app_hostname_key UNIQUE (app_id, hostname),
    CONSTRAINT app_domains_status_known CHECK (status IN ('pending', 'issuing', 'active', 'issue_failed', 'detached'))
);

CREATE UNIQUE INDEX IF NOT EXISTS app_domains_routed_hostname
    ON app_domains (hostname) WHERE status IN ('issuing', 'active', 'issue_failed');
CREATE INDEX IF NOT EXISTS idx_app_domains_app ON app_domains (project_id, app_id);
