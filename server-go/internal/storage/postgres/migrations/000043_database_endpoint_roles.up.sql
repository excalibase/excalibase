-- A project may hold two public ports (EXC-409).
--
-- A DocumentDB project serves Postgres and the MongoDB wire protocol from the
-- one pod, and when it publishes it needs a port for each. They come from the
-- same allocator as before — the same window, the same advisory lock, the same
-- thirty-day quarantine on release — because the reason a freed port is held
-- back does not change with the protocol behind it.
--
-- So the table gains a role and is keyed by (project_id, role) rather than by
-- project alone. Every row that exists today is a Postgres port, which is what
-- the default says; there is no second migration path because there is nothing
-- else it could have been.
--
-- The unique index on port is deliberately left exactly as it was. It is
-- partial on the whole table, not per role, so it goes on being the backstop
-- that stops two projects holding one port — and now also stops one project's
-- Mongo port colliding with another's Postgres port.
ALTER TABLE database_endpoints
    ADD COLUMN IF NOT EXISTS role TEXT NOT NULL DEFAULT 'postgres';

ALTER TABLE database_endpoints
    DROP CONSTRAINT IF EXISTS database_endpoints_pkey;

ALTER TABLE database_endpoints
    ADD PRIMARY KEY (project_id, role);

-- A role the allocator does not recognise would be a port held forever by
-- nobody: no caller reads it back, so no caller ever releases it.
ALTER TABLE database_endpoints
    DROP CONSTRAINT IF EXISTS database_endpoints_role_known;
ALTER TABLE database_endpoints
    ADD CONSTRAINT database_endpoints_role_known CHECK (role IN ('postgres', 'mongo'));
