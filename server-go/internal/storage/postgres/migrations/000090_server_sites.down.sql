ALTER TABLE tier_configs DROP COLUMN IF EXISTS max_site_server_bundle_bytes;
ALTER TABLE tier_configs DROP COLUMN IF EXISTS max_server_sites;
DROP TABLE IF EXISTS site_deploy_code;
ALTER TABLE site_deploys DROP COLUMN IF EXISTS server_config;
ALTER TABLE site_deploys DROP COLUMN IF EXISTS kind;
ALTER TABLE sites DROP COLUMN IF EXISTS kind;
