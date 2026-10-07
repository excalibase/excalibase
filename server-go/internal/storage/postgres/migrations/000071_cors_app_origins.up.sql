-- The origin an app added to its project's CORS allowlist (EXC-544), keyed by
-- app id, so deleting the app removes that origin and nothing a person added.
ALTER TABLE project_cors_settings
    ADD COLUMN IF NOT EXISTS app_origins JSONB NOT NULL DEFAULT '{}'::jsonb;
