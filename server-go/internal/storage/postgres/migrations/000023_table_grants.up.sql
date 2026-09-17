-- Table / function exposure grants (EXC-370).
--
-- Deny-by-default exposure list for the generated GraphQL + REST surface.
-- excalibase-graphql fetches these through
-- GET /api/provision/{projectId}/table-grants/ alongside the RLS row and
-- column policies it already caches, and evicts all three together on the
-- "policies.{projectId}.changed" NATS subject.
--
-- Backward compatibility is the whole point of the second table: an empty
-- grant list is ambiguous on its own ("nothing granted" vs "exposure was
-- never configured"), so enforcement is recorded explicitly per project.
-- No row in project_exposure_settings, or a row with enforced = FALSE,
-- means the engine must NOT enforce exposure — every project that exists
-- before this migration keeps its current surface after an upgrade.

CREATE TABLE IF NOT EXISTS table_grants (
    id          TEXT PRIMARY KEY,
    project_id  TEXT NOT NULL,
    resource    TEXT NOT NULL,                      -- schema-qualified table or function, e.g. public.orders
    operations  TEXT[] NOT NULL,                    -- {'SELECT','INSERT','UPDATE','DELETE'}
    role_name   TEXT NOT NULL,                      -- role the grant is issued to ('*' = every role)
    enabled     BOOLEAN NOT NULL DEFAULT TRUE,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT table_grants_operations_not_empty CHECK (cardinality(operations) > 0)
);

CREATE INDEX IF NOT EXISTS idx_table_grants_project_resource ON table_grants(project_id, resource);

-- One grant row per (project, resource, role); operations are a set on that row.
CREATE UNIQUE INDEX IF NOT EXISTS ux_table_grants_project_resource_role
    ON table_grants(project_id, resource, role_name);

CREATE TABLE IF NOT EXISTS project_exposure_settings (
    project_id TEXT PRIMARY KEY,
    enforced   BOOLEAN NOT NULL DEFAULT FALSE,      -- opt-in: absent row and FALSE both mean "do not enforce"
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
