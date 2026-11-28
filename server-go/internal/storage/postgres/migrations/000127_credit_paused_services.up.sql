-- What a used-up credit pauses (EXC-866 C.5). One row per service paused for credit, so adding
-- credit resumes exactly those and nothing a person paused themselves. Modelled on
-- abuse_suspension_apps. No foreign keys: a row for a deleted service is dropped by the sweep.
-- A database's service_id is its project id; an app's is its app id.
CREATE TABLE IF NOT EXISTS credit_paused_services (
    org_id            TEXT        NOT NULL,
    kind              TEXT        NOT NULL CHECK (kind IN ('database', 'app', 'functions')),
    project_id        TEXT        NOT NULL,
    service_id        TEXT        NOT NULL,
    service_name      TEXT        NOT NULL DEFAULT '',
    previous_replicas INTEGER     NOT NULL DEFAULT 0 CHECK (previous_replicas >= 0),
    paused_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (org_id, kind, project_id, service_id)
);
CREATE INDEX IF NOT EXISTS idx_credit_paused_services_service ON credit_paused_services (project_id, kind, service_id);

-- When an org's credit ran out and has not come back: the grace period counts from it.
CREATE TABLE IF NOT EXISTS credit_zero_orgs (
    org_id TEXT        PRIMARY KEY,
    since  TIMESTAMPTZ NOT NULL DEFAULT now()
);
