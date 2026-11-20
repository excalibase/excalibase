-- Project secrets a server-rendered site's code may read (EXC-661, ADR 0045
-- decision 3), the third marking beside build-visible. Absent = not visible.
CREATE TABLE IF NOT EXISTS site_server_visible_secrets (
    project_id TEXT NOT NULL,
    name       TEXT NOT NULL,
    marked_by  TEXT NOT NULL,
    marked_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (project_id, name)
);
