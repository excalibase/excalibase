-- The old vocabulary has no custom roles; those grants are dropped rather than
-- mapped, because mapping them onto an end-user role would widen exposure.
ALTER TABLE table_grants
    DROP CONSTRAINT IF EXISTS table_grants_role_is_grantable;

DELETE FROM table_grants WHERE role_name NOT IN ('anon', 'user');

UPDATE table_grants SET role_name = 'authenticated' WHERE role_name = 'user';

ALTER TABLE table_grants
    ADD CONSTRAINT table_grants_role_is_end_user
    CHECK (role_name IN ('anon', 'authenticated'));
