-- Grants name the role the engine runs the caller as (EXC-370): anon, user
-- (a signed-in end user, formerly 'authenticated') or a custom role from the
-- token. service bypasses exposure, so a grant to it would mean nothing.
ALTER TABLE table_grants
    DROP CONSTRAINT IF EXISTS table_grants_role_is_end_user;

UPDATE table_grants SET role_name = 'user' WHERE role_name = 'authenticated';

ALTER TABLE table_grants
    ADD CONSTRAINT table_grants_role_is_grantable
    CHECK (role_name ~ '^[a-z][a-z0-9_]{0,62}$' AND role_name <> 'service');
