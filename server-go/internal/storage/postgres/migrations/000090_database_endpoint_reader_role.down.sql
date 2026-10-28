DELETE FROM database_endpoints WHERE role = 'reader';
ALTER TABLE database_endpoints
    DROP CONSTRAINT IF EXISTS database_endpoints_role_known;
ALTER TABLE database_endpoints
    ADD CONSTRAINT database_endpoints_role_known CHECK (role IN ('postgres', 'mongo'));
