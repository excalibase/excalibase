-- Customer applications (EXC-377, EXC-378): a container image the platform
-- runs beside a project's database.
--
-- Apps are a collection keyed by app id, not a single column on the project.
-- One app per project is the rule today, but the rule lives in the store
-- (apphost.MaxAppsPerProject, enforced inside the admitting transaction), so
-- raising it later is a constant, not a migration. A project may hold an app
-- and no database, so nothing here references database_instances: the two
-- services are independent.
--
-- `doc` holds the whole App record as JSON, as the edge-function record does:
-- the struct carries optional fields today and the deploy lifecycle will add
-- more, and a column-per-field schema would silently drop whatever was added
-- last. The columns beside it are extracted only for keys, ordering and the
-- per-project count.

CREATE TABLE IF NOT EXISTS apps (
    project_id TEXT NOT NULL,
    id         TEXT NOT NULL,               -- app id, unique per project
    name       TEXT NOT NULL,               -- human name, unique per project
    status     TEXT NOT NULL,
    version    INTEGER NOT NULL DEFAULT 1,
    doc        JSONB NOT NULL,              -- full App record
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (project_id, id),
    CONSTRAINT apps_project_name_key UNIQUE (project_id, name)
);

-- The per-project count the app limit is decided on, and the project listing.
CREATE INDEX IF NOT EXISTS idx_apps_project ON apps(project_id);
