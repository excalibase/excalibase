-- Provisioning control-plane store. Kept in its own database, separate from
-- the tenant/data-plane database (`hana`), so platform metadata (orgs,
-- projects, users, audit, vault) never mixes with application tables.
CREATE DATABASE excalibase_platform OWNER hana001;
