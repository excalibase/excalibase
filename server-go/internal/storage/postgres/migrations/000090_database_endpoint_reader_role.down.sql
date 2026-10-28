-- Refused while a read port is held: deleting its row would leave its Service
-- answering untracked and free its port without quarantine. Close read ports first.
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM database_endpoints WHERE role = 'reader' AND (port IS NOT NULL OR public)) THEN
        RAISE EXCEPTION 'close every database read port before rolling back 000090';
    END IF;
END
$$;
DELETE FROM database_endpoints WHERE role = 'reader';
ALTER TABLE database_endpoints
    DROP CONSTRAINT IF EXISTS database_endpoints_role_known;
ALTER TABLE database_endpoints
    ADD CONSTRAINT database_endpoints_role_known CHECK (role IN ('postgres', 'mongo'));
