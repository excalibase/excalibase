-- How many apps one project may hold on each plan (EXC-524, owner 2026-09-29).
-- Defaults an admin may edit with the rest of the plan; 0 offers no apps.
ALTER TABLE tier_configs ADD COLUMN max_apps INTEGER;
UPDATE tier_configs SET max_apps = 2 WHERE tier = 'FREE';
UPDATE tier_configs SET max_apps = 5 WHERE tier = 'STANDARD';
UPDATE tier_configs SET max_apps = 20 WHERE tier = 'ENTERPRISE';
UPDATE tier_configs SET max_apps = 0 WHERE max_apps IS NULL;
ALTER TABLE tier_configs ALTER COLUMN max_apps SET NOT NULL;
