-- The disk a project's cluster asks for, recorded so a restore can size the
-- new cluster to it (EXC-492). Existing projects never grew: their plan's
-- starting disk is their disk.
ALTER TABLE database_instances ADD COLUMN storage_size TEXT;
UPDATE database_instances d SET storage_size = t.storage_size
    FROM tier_configs t WHERE t.tier = d.tier AND d.storage_size IS NULL;
UPDATE database_instances SET storage_size = '5Gi' WHERE storage_size IS NULL;
ALTER TABLE database_instances ALTER COLUMN storage_size SET NOT NULL;
