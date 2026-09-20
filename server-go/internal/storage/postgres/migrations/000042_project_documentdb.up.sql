-- Whether a project's database carries the DocumentDB extension (EXC-409).
--
-- Create-time only. The cluster has to preload pg_documentdb and its
-- companions before the extension will load at all, and a cluster's preloaded
-- libraries are decided when it is provisioned — so a project is created with
-- DocumentDB or without it, and nothing turns it on later. The UPDATE
-- statement leaves this column out for exactly that reason, the way it leaves
-- out project_id and org_id.
--
-- FALSE is the honest default for every project that already exists: none of
-- them was provisioned with the libraries preloaded, so none of them has the
-- extension, whatever major their image happens to carry.
ALTER TABLE database_instances
    ADD COLUMN IF NOT EXISTS documentdb BOOLEAN NOT NULL DEFAULT FALSE;
