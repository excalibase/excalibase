-- Git builds of apps (EXC-547, ADR 0037 decisions 8-10): one build's plan
-- limits, edited by an admin with the rest of the plan. A separate allowance
-- from what the project runs. Starting numbers are the gVisor app-build spike's.
ALTER TABLE tier_configs ADD COLUMN app_build_cpu_millis INTEGER NOT NULL DEFAULT 2000;
ALTER TABLE tier_configs ADD COLUMN app_build_memory TEXT NOT NULL DEFAULT '4Gi';
ALTER TABLE tier_configs ADD COLUMN app_build_disk TEXT NOT NULL DEFAULT '10Gi';
ALTER TABLE tier_configs ADD COLUMN app_build_timeout_seconds INTEGER NOT NULL DEFAULT 900;
ALTER TABLE tier_configs ADD COLUMN app_build_concurrency INTEGER NOT NULL DEFAULT 1;
ALTER TABLE tier_configs ADD COLUMN app_build_max_image TEXT NOT NULL DEFAULT '2Gi';
UPDATE tier_configs SET app_build_cpu_millis = 4000, app_build_memory = '8Gi', app_build_disk = '20Gi',
    app_build_timeout_seconds = 1800, app_build_concurrency = 2, app_build_max_image = '5Gi'
    WHERE tier IN ('STANDARD', 'ENTERPRISE');
