-- The largest disk one app may have on each plan (EXC-523). Defaults an admin
-- may edit with the rest of the plan; 0Gi offers no app disks.
ALTER TABLE tier_configs ADD COLUMN max_app_disk_size TEXT;
UPDATE tier_configs SET max_app_disk_size = '1Gi' WHERE tier = 'FREE';
UPDATE tier_configs SET max_app_disk_size = '20Gi' WHERE tier = 'STANDARD';
UPDATE tier_configs SET max_app_disk_size = '100Gi' WHERE tier = 'ENTERPRISE';
UPDATE tier_configs SET max_app_disk_size = '0Gi' WHERE max_app_disk_size IS NULL;
ALTER TABLE tier_configs ALTER COLUMN max_app_disk_size SET NOT NULL;
