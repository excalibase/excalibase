package service

// endUserSessionsRevokeSQL ends what the source project issued the end users
// of a copy restored from its backup: refresh tokens, unredeemed reset and
// verification links, sign-ins in flight (EXC-662) and api keys. The users
// are kept (EXC-635). Tables the auth service has not created yet are skipped.
const endUserSessionsRevokeSQL = `
DO $$
BEGIN
    IF to_regclass('auth.refresh_tokens') IS NOT NULL THEN
        DELETE FROM auth.refresh_tokens;
    END IF;
    IF to_regclass('auth.password_reset_tokens') IS NOT NULL THEN
        DELETE FROM auth.password_reset_tokens;
    END IF;
    IF to_regclass('auth.email_verification_tokens') IS NOT NULL THEN
        DELETE FROM auth.email_verification_tokens;
    END IF;
    IF to_regclass('auth.email_otps') IS NOT NULL THEN
        DELETE FROM auth.email_otps;
    END IF;
    IF to_regclass('auth.auth_codes') IS NOT NULL THEN
        DELETE FROM auth.auth_codes;
    END IF;
    IF to_regclass('auth.flow_states') IS NOT NULL THEN
        DELETE FROM auth.flow_states;
    END IF;
    IF to_regclass('auth.api_keys') IS NOT NULL THEN
        UPDATE auth.api_keys SET revoked_at = now() WHERE revoked_at IS NULL;
    END IF;
END $$;
`
