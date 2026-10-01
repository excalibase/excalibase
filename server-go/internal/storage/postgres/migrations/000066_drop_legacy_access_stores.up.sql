-- Permissions are the only access store (EXC-400). The engine reads
-- GET /api/provision/{projectId}/permissions/ and nothing else, so the row
-- policies, column policies and table grants it used to read are dead, and so
-- is the marker of the one-shot fold that copied them into permissions.
-- There are no running tenants (ADR 0034): nothing is carried over.
DROP TABLE IF EXISTS legacy_permissions_migrated;
DROP TABLE IF EXISTS table_grants;
DROP TABLE IF EXISTS column_policies;
DROP TABLE IF EXISTS rls_policies;
