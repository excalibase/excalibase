ALTER TABLE project_auth_settings
    DROP COLUMN IF EXISTS guest_captcha_secret_set,
    DROP COLUMN IF EXISTS guest_sign_in_enabled;
