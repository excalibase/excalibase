DELETE FROM site_builds WHERE trigger = 'preview';
DROP INDEX IF EXISTS idx_site_builds_preview;
ALTER TABLE site_builds DROP CONSTRAINT IF EXISTS site_builds_trigger_check;
ALTER TABLE site_builds ADD CONSTRAINT site_builds_trigger_check CHECK (trigger IN ('push', 'rebuild'));
ALTER TABLE site_builds DROP COLUMN IF EXISTS preview_id;
ALTER TABLE site_git_connections DROP COLUMN IF EXISTS preview_settings;
DROP TABLE IF EXISTS site_previews;
ALTER TABLE tier_configs DROP COLUMN IF EXISTS max_site_previews;
