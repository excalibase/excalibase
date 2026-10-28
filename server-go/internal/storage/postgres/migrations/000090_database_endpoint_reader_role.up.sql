-- A project's read-only address holds a public port of its own (EXC-666),
-- from the same allocator, window and quarantine as its other ports.
ALTER TABLE database_endpoints
    DROP CONSTRAINT IF EXISTS database_endpoints_role_known;
ALTER TABLE database_endpoints
    ADD CONSTRAINT database_endpoints_role_known CHECK (role IN ('postgres', 'mongo', 'reader'));
