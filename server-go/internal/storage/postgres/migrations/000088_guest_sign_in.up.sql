-- Guest accounts (EXC-662): the project's switch, and whether the vault holds
-- its Turnstile secret. Additive and unread while authguest is off.
ALTER TABLE project_auth_settings
    ADD COLUMN IF NOT EXISTS guest_sign_in_enabled BOOLEAN NOT NULL DEFAULT false,
    ADD COLUMN IF NOT EXISTS guest_captcha_secret_set BOOLEAN NOT NULL DEFAULT false;
