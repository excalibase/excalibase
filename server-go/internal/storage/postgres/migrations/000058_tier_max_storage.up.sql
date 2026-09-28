-- Each plan has a disk a project starts with (storage_size) and a disk it may
-- grow to (max_storage_size) (EXC-492). Free is fixed at its starting disk.
ALTER TABLE tier_configs ADD COLUMN max_storage_size TEXT;
UPDATE tier_configs SET max_storage_size = '500Gi' WHERE tier = 'STANDARD';
UPDATE tier_configs SET max_storage_size = '2Ti' WHERE tier = 'ENTERPRISE';
UPDATE tier_configs SET max_storage_size = storage_size WHERE max_storage_size IS NULL;
ALTER TABLE tier_configs ALTER COLUMN max_storage_size SET NOT NULL;
