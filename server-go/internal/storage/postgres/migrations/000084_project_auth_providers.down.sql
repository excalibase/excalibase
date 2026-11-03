DROP TABLE IF EXISTS project_auth_providers;
ALTER TABLE project_auth_settings
    DROP COLUMN IF EXISTS allow_new_users,
    DROP COLUMN IF EXISTS redirect_urls;
