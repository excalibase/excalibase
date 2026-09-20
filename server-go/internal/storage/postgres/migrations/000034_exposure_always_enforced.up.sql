-- Exposure enforcement is always on (EXC-400).
--
-- project_exposure_settings was a per-project opt-in whose ABSENCE meant
-- "serve this project's schema unfiltered". Provisioning never wrote the row,
-- so enforcement was off for every project that existed and an operator who
-- granted one table changed nothing. A grant that changes nothing is the bug,
-- and a default-off flag no code sets is not a safety valve — it is a hole.
--
-- Enforcement is now a platform decision carried in config
-- (EXCALIBASE_EXPOSURE_ENFORCED, on by default) and reported explicitly on the
-- wire. There is no per-project state left to keep.
DROP TABLE IF EXISTS project_exposure_settings;

-- Grants name end users only: anon (no session) or authenticated (a session).
-- Anything else — '*', 'user', a bespoke role name — belongs in an RLS policy,
-- where a claim-driven rule can be expressed without making the reachable
-- schema itself depend on a free-form claim.
--
-- There are no running tenants, so rows outside that vocabulary are dead
-- configuration from the opt-in era; they are removed rather than guessed at,
-- because mapping '*' onto both end-user roles would silently WIDEN exposure.
DELETE FROM table_grants WHERE role_name NOT IN ('anon', 'authenticated');

ALTER TABLE table_grants
    DROP CONSTRAINT IF EXISTS table_grants_role_is_end_user;

ALTER TABLE table_grants
    ADD CONSTRAINT table_grants_role_is_end_user
    CHECK (role_name IN ('anon', 'authenticated'));
