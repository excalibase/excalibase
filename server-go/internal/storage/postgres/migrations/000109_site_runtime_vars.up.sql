-- A server site's runtime variables (EXC-661, ADR 0045 decision 3): plain text the
-- site's code reads from its env. A deploy snapshots them with its settings, so
-- a rollback runs under the variables of the deploy it restores.
CREATE TABLE IF NOT EXISTS site_runtime_vars (
    site_id    TEXT PRIMARY KEY REFERENCES sites(id) ON DELETE CASCADE,
    vars       JSONB NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
