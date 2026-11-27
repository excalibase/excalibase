-- The auth service's postgres migrations (excalibase-auth, internal/migrate/migrations/postgres, *.up.sql in order).
-- A test applies them and expects the repair to find nothing foreign in them. Update with the auth migrations.
CREATE SCHEMA IF NOT EXISTS auth;
SET search_path TO auth;

CREATE TABLE IF NOT EXISTS users (
    id BIGSERIAL PRIMARY KEY,
    email VARCHAR(100) NOT NULL UNIQUE,
    password VARCHAR(255) NOT NULL,
    full_name VARCHAR(100) NOT NULL,
    role VARCHAR(50) NOT NULL DEFAULT 'user',
    enabled BOOLEAN NOT NULL DEFAULT true,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    last_login_at TIMESTAMPTZ
);

CREATE TABLE IF NOT EXISTS refresh_tokens (
    id BIGSERIAL PRIMARY KEY,
    token VARCHAR(255) NOT NULL UNIQUE,
    user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    expiry_date TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    revoked BOOLEAN NOT NULL DEFAULT false
);

CREATE INDEX IF NOT EXISTS idx_users_email ON users(email);
CREATE INDEX IF NOT EXISTS idx_refresh_tokens_token ON refresh_tokens(token);
CREATE INDEX IF NOT EXISTS idx_refresh_tokens_user_id ON refresh_tokens(user_id);
-- Schema-qualify all names so the migration is safe under transaction-pooled
-- connections (PgBouncer transaction mode) where session-level SET search_path
-- can be lost mid-migration.

CREATE TABLE IF NOT EXISTS auth.api_keys (
    id           BIGSERIAL PRIMARY KEY,
    key_hash     VARCHAR(64) NOT NULL UNIQUE,           -- SHA-256 hex (64 chars)
    key_prefix   VARCHAR(24) NOT NULL,                  -- first 12 chars of random portion, for display
    key_type     VARCHAR(16) NOT NULL CHECK (key_type IN ('publishable','secret')),
    name         VARCHAR(100),
    created_by   BIGINT REFERENCES auth.users(id) ON DELETE CASCADE,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    last_used_at TIMESTAMPTZ,
    revoked_at   TIMESTAMPTZ,
    revoked_by   BIGINT REFERENCES auth.users(id) ON DELETE SET NULL
);

CREATE INDEX IF NOT EXISTS idx_api_keys_prefix ON auth.api_keys(key_prefix) WHERE revoked_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_api_keys_created_by ON auth.api_keys(created_by);
-- EXC-11: end-user email verification.
--
-- Schema-qualify every name so the migration is safe under transaction-pooled
-- connections where a session-level SET search_path can be lost mid-migration.

ALTER TABLE auth.users ADD COLUMN IF NOT EXISTS email_verified BOOLEAN NOT NULL DEFAULT false;

-- Accounts that predate verification were created when no proof of address was
-- ever requested. Grandfather them in so a project that later switches on
-- requireEmailVerification does not lock out its whole existing user base.
-- This runs once, against exactly the rows present at migration time.
UPDATE auth.users SET email_verified = true;

CREATE TABLE IF NOT EXISTS auth.email_verification_tokens (
    id          BIGSERIAL PRIMARY KEY,
    token_hash  CHAR(64) NOT NULL UNIQUE,              -- SHA-256 hex of the emailed token
    user_id     BIGINT NOT NULL REFERENCES auth.users(id) ON DELETE CASCADE,
    expires_at  TIMESTAMPTZ NOT NULL,
    consumed_at TIMESTAMPTZ,                           -- non-null once redeemed: single use
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_email_verification_tokens_user_id
    ON auth.email_verification_tokens(user_id);
-- EXC-12: end-user password reset.
--
-- Same shape as email_verification_tokens: only the SHA-256 hash of the emailed
-- token is stored, consumed_at makes it single use, and the row is schema
-- qualified so the migration is safe under transaction pooling.

CREATE TABLE IF NOT EXISTS auth.password_reset_tokens (
    id          BIGSERIAL PRIMARY KEY,
    token_hash  CHAR(64) NOT NULL UNIQUE,
    user_id     BIGINT NOT NULL REFERENCES auth.users(id) ON DELETE CASCADE,
    expires_at  TIMESTAMPTZ NOT NULL,
    consumed_at TIMESTAMPTZ,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_password_reset_tokens_user_id
    ON auth.password_reset_tokens(user_id);
-- Refresh tokens are kept only as SHA-256 hashes, and every token belongs to a
-- family (one login) so replaying a rotated token can revoke the whole session.

ALTER TABLE auth.refresh_tokens ADD COLUMN IF NOT EXISTS token_hash CHAR(64);
ALTER TABLE auth.refresh_tokens ADD COLUMN IF NOT EXISTS family_id UUID;

UPDATE auth.refresh_tokens
SET token_hash = encode(sha256(convert_to(token, 'UTF8')), 'hex'),
    family_id  = gen_random_uuid()
WHERE token_hash IS NULL;

ALTER TABLE auth.refresh_tokens ALTER COLUMN token_hash SET NOT NULL;
ALTER TABLE auth.refresh_tokens ALTER COLUMN family_id SET NOT NULL;
ALTER TABLE auth.refresh_tokens DROP COLUMN IF EXISTS token;

CREATE UNIQUE INDEX IF NOT EXISTS idx_refresh_tokens_token_hash ON auth.refresh_tokens(token_hash);
CREATE INDEX IF NOT EXISTS idx_refresh_tokens_family_id ON auth.refresh_tokens(family_id);
-- EXC-370: an account may switch between several roles, and every change of
-- an account's roles is recorded with who made it.

-- A role name is up to 63 characters (a Postgres identifier).
ALTER TABLE auth.users ALTER COLUMN role TYPE VARCHAR(63);

-- NULL means the account may act only as users.role.
ALTER TABLE auth.users ADD COLUMN IF NOT EXISTS allowed_roles TEXT[];

CREATE TABLE IF NOT EXISTS auth.role_changes (
    id                BIGSERIAL PRIMARY KEY,
    user_id           BIGINT NOT NULL REFERENCES auth.users(id) ON DELETE CASCADE,
    old_role          TEXT NOT NULL,
    new_role          TEXT NOT NULL,
    old_allowed_roles TEXT[] NOT NULL,
    new_allowed_roles TEXT[] NOT NULL,
    actor             TEXT NOT NULL,              -- studio:<platform user id> | service-key:<api key id>
    changed_at        TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_role_changes_user_id ON auth.role_changes(user_id);
-- EXC-557: a verification link proves the address; only a link minted by the
-- sign-up that set the current password proves that password too. Links from
-- resend (and any minted before this column) lead to a password reset.
ALTER TABLE auth.email_verification_tokens
    ADD COLUMN IF NOT EXISTS confirms_password BOOLEAN NOT NULL DEFAULT false;
-- EXC-662: sign-in through Google or GitHub. Accounts made by a provider carry
-- no password. Anonymous accounts of a later step carry no address; nothing
-- writes one without an address yet.

ALTER TABLE auth.users ALTER COLUMN password DROP NOT NULL;
ALTER TABLE auth.users ALTER COLUMN email DROP NOT NULL;

-- When the address was proven. 000003 marked every older account verified with
-- no proof; only a redeemed link or a provider sign-in sets this, and only an
-- account with it is linked to a provider sign-in.
ALTER TABLE auth.users ADD COLUMN IF NOT EXISTS email_verified_at TIMESTAMPTZ;
UPDATE auth.users u SET email_verified_at = proof.at
FROM (SELECT user_id, max(consumed_at) AS at FROM auth.email_verification_tokens
      WHERE consumed_at IS NOT NULL GROUP BY user_id) proof
WHERE u.id = proof.user_id AND u.email_verified AND u.email_verified_at IS NULL;

-- A provider account (provider + its stable subject id) joined to one user.
CREATE TABLE IF NOT EXISTS auth.identities (
    id              BIGSERIAL PRIMARY KEY,
    user_id         BIGINT NOT NULL REFERENCES auth.users(id) ON DELETE CASCADE,
    provider        VARCHAR(32)  NOT NULL,
    subject         VARCHAR(255) NOT NULL,
    email           VARCHAR(255),
    email_verified  BOOLEAN NOT NULL DEFAULT false,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    last_sign_in_at TIMESTAMPTZ,
    CONSTRAINT uq_identities_provider_subject UNIQUE (provider, subject)
);
CREATE INDEX IF NOT EXISTS idx_identities_user_id ON auth.identities(user_id);

-- A sign-in between /authorize and the provider's callback; the state is
-- stored only as its SHA-256 and spent once.
CREATE TABLE IF NOT EXISTS auth.flow_states (
    state_hash            CHAR(64) PRIMARY KEY,
    provider              VARCHAR(32)  NOT NULL,
    idp_code_verifier     VARCHAR(128) NOT NULL,
    nonce                 VARCHAR(128) NOT NULL DEFAULT '',
    client_code_challenge VARCHAR(128) NOT NULL,
    redirect_to           TEXT NOT NULL,
    link_user_id          BIGINT REFERENCES auth.users(id) ON DELETE CASCADE,
    expires_at            TIMESTAMPTZ NOT NULL,
    created_at            TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_flow_states_expires_at ON auth.flow_states(expires_at);

-- The code handed back to the app, redeemed once with the PKCE verifier.
CREATE TABLE IF NOT EXISTS auth.auth_codes (
    code_hash      CHAR(64) PRIMARY KEY,
    user_id        BIGINT NOT NULL REFERENCES auth.users(id) ON DELETE CASCADE,
    code_challenge VARCHAR(128) NOT NULL,
    expires_at     TIMESTAMPTZ NOT NULL,
    consumed_at    TIMESTAMPTZ,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_auth_codes_expires_at ON auth.auth_codes(expires_at);
-- EXC-662: provider sign-ins match an address whatever its case.
CREATE INDEX IF NOT EXISTS idx_users_email_lower ON auth.users (lower(email));
-- EXC-662: sign-in with an emailed link or six-digit code. One row per send: a
-- link is kept as its SHA-256, a code as an HMAC under a key the database never
-- holds. Rows stay a day so the send budgets can be counted from them.
CREATE TABLE IF NOT EXISTS auth.email_otps (
    id             BIGSERIAL PRIMARY KEY,
    email          VARCHAR(255) NOT NULL,
    kind           VARCHAR(16)  NOT NULL,
    link_hash      CHAR(64),
    code_hmac      CHAR(64),
    code_challenge VARCHAR(128) NOT NULL,
    create_user    BOOLEAN NOT NULL DEFAULT false,
    mailed         BOOLEAN NOT NULL DEFAULT false,
    ip             VARCHAR(64),
    attempts       INT NOT NULL DEFAULT 0,
    expires_at     TIMESTAMPTZ NOT NULL,
    consumed_at    TIMESTAMPTZ,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT uq_email_otps_link_hash UNIQUE (link_hash)
);
CREATE INDEX IF NOT EXISTS idx_email_otps_email ON auth.email_otps (lower(email), created_at);
CREATE INDEX IF NOT EXISTS idx_email_otps_created_at ON auth.email_otps (created_at);
CREATE INDEX IF NOT EXISTS idx_email_otps_ip ON auth.email_otps (ip, created_at);

-- Every send locks this one row, so replicas count the budgets one at a time.
CREATE TABLE IF NOT EXISTS auth.email_otp_lock (
    id SMALLINT PRIMARY KEY CHECK (id = 1)
);
INSERT INTO auth.email_otp_lock (id) VALUES (1) ON CONFLICT DO NOTHING;

-- Who signed in, how and from where. No foreign key: the trail outlives an
-- account that is replaced or deleted.
CREATE TABLE IF NOT EXISTS auth.audit_log (
    id         BIGSERIAL PRIMARY KEY,
    user_id    BIGINT,
    event      VARCHAR(32) NOT NULL,
    method     VARCHAR(32),
    ip         VARCHAR(64),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_audit_log_user_id ON auth.audit_log (user_id);
CREATE INDEX IF NOT EXISTS idx_audit_log_created_at ON auth.audit_log (created_at);
-- EXC-662: a signed-in user links Google, GitHub or another address to the
-- account. Linking needs a recent sign-in, so a session keeps when its owner
-- signed in across rotations; a link is written only when the app redeems its
-- code or spends its email code with the PKCE verifier.
ALTER TABLE auth.refresh_tokens ADD COLUMN IF NOT EXISTS last_auth_at TIMESTAMPTZ;

ALTER TABLE auth.auth_codes
    ADD COLUMN IF NOT EXISTS link_provider VARCHAR(32),
    ADD COLUMN IF NOT EXISTS link_subject  VARCHAR(255),
    ADD COLUMN IF NOT EXISTS link_email    VARCHAR(255);

ALTER TABLE auth.email_otps
    ADD COLUMN IF NOT EXISTS link_user_id BIGINT REFERENCES auth.users(id) ON DELETE CASCADE;
-- EXC-662: end users read and change their own account. Metadata stays out of
-- the token; user_metadata is the user's to write, app_metadata only the
-- project's (secret key or Studio). NULL reads as {}.
ALTER TABLE auth.users
    ADD COLUMN IF NOT EXISTS user_metadata JSONB,
    ADD COLUMN IF NOT EXISTS app_metadata  JSONB;

-- A requested change of address, proven by a link mailed to the new one. The
-- link is kept as its SHA-256; rows stay a day so the budgets count from them.
CREATE TABLE IF NOT EXISTS auth.email_change_tokens (
    id          BIGSERIAL PRIMARY KEY,
    user_id     BIGINT NOT NULL REFERENCES auth.users(id) ON DELETE CASCADE,
    new_email   VARCHAR(100) NOT NULL,
    token_hash  CHAR(64) NOT NULL,
    mailed      BOOLEAN NOT NULL DEFAULT false,
    expires_at  TIMESTAMPTZ NOT NULL,
    consumed_at TIMESTAMPTZ,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT uq_email_change_tokens_token_hash UNIQUE (token_hash)
);
CREATE INDEX IF NOT EXISTS idx_email_change_tokens_user_id ON auth.email_change_tokens (user_id, created_at);
CREATE INDEX IF NOT EXISTS idx_email_change_tokens_created_at ON auth.email_change_tokens (created_at);
-- EXC-662: guest accounts. A guest has no address and no password until it is
-- upgraded by proving an address or linking a provider; it keeps its id.
ALTER TABLE auth.users ADD COLUMN IF NOT EXISTS is_guest BOOLEAN NOT NULL DEFAULT false;
CREATE INDEX IF NOT EXISTS idx_users_guests ON auth.users (id) WHERE is_guest;

-- One row per guest sign-up, kept a day so the per-client and per-project
-- budgets are counted the same on every replica.
CREATE TABLE IF NOT EXISTS auth.guest_sign_ups (
    id         BIGSERIAL PRIMARY KEY,
    ip         VARCHAR(64) NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_guest_sign_ups_ip ON auth.guest_sign_ups (ip, created_at);
CREATE INDEX IF NOT EXISTS idx_guest_sign_ups_created_at ON auth.guest_sign_ups (created_at);

-- Every guest sign-up locks this one row so the budgets are counted one at a
-- time; cleaned_at is when inactive guests were last removed.
CREATE TABLE IF NOT EXISTS auth.guest_lock (
    id         SMALLINT PRIMARY KEY CHECK (id = 1),
    cleaned_at TIMESTAMPTZ
);
INSERT INTO auth.guest_lock (id) VALUES (1) ON CONFLICT DO NOTHING;

-- A guest's address change records its client, capped per client and held
-- to a share of the project's mail budget.
ALTER TABLE auth.email_change_tokens ADD COLUMN IF NOT EXISTS guest_client VARCHAR(64);
CREATE INDEX IF NOT EXISTS idx_email_change_tokens_guest ON auth.email_change_tokens (guest_client, created_at)
    WHERE guest_client IS NOT NULL;
-- EXC-662: TOTP second factors. A secret is stored sealed with the project's
-- key (AES-256-GCM bound to the account and factor), never in the clear.
CREATE TABLE IF NOT EXISTS auth.mfa_factors (
    id             UUID         PRIMARY KEY,
    user_id        BIGINT       NOT NULL REFERENCES auth.users(id) ON DELETE CASCADE,
    factor_type    VARCHAR(16)  NOT NULL DEFAULT 'totp' CHECK (factor_type = 'totp'),
    friendly_name  VARCHAR(100) NOT NULL,
    secret_enc     BYTEA        NOT NULL,
    status         VARCHAR(16)  NOT NULL DEFAULT 'unverified' CHECK (status IN ('unverified', 'verified')),
    -- The newest time step a code was accepted for; no step is accepted twice.
    last_used_step BIGINT,
    created_at     TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    updated_at     TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    CONSTRAINT uq_mfa_factors_name UNIQUE (user_id, friendly_name)
);

CREATE TABLE IF NOT EXISTS auth.mfa_challenges (
    id         UUID        PRIMARY KEY,
    factor_id  UUID        NOT NULL REFERENCES auth.mfa_factors(id) ON DELETE CASCADE,
    ip         VARCHAR(64),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    expires_at TIMESTAMPTZ NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_mfa_challenges_factor ON auth.mfa_challenges (factor_id, expires_at);

-- Recovery codes are kept as keyed hashes and spent once.
CREATE TABLE IF NOT EXISTS auth.mfa_recovery_codes (
    id         BIGSERIAL   PRIMARY KEY,
    user_id    BIGINT      NOT NULL REFERENCES auth.users(id) ON DELETE CASCADE,
    code_hash  CHAR(64)    NOT NULL,
    used_at    TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT uq_mfa_recovery_codes UNIQUE (user_id, code_hash)
);

-- Wrong codes, counted here so every replica shares one budget: per factor,
-- or per account (factor_id NULL) for recovery codes.
CREATE TABLE IF NOT EXISTS auth.mfa_failures (
    id        BIGSERIAL   PRIMARY KEY,
    user_id   BIGINT      NOT NULL REFERENCES auth.users(id) ON DELETE CASCADE,
    factor_id UUID        REFERENCES auth.mfa_factors(id) ON DELETE CASCADE,
    failed_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_mfa_failures_user ON auth.mfa_failures (user_id, failed_at);
CREATE INDEX IF NOT EXISTS idx_mfa_failures_factor ON auth.mfa_failures (factor_id);

-- A session's assurance level and how it signed in, kept across refreshes.
ALTER TABLE auth.refresh_tokens
    ADD COLUMN IF NOT EXISTS aal SMALLINT NOT NULL DEFAULT 1,
    ADD COLUMN IF NOT EXISTS amr VARCHAR(100) NOT NULL DEFAULT '';
-- EXC-662: wrong codes are budgeted per session (refresh family) too, so one
-- session that spent its budget does not lock the account's other sessions.
ALTER TABLE auth.mfa_failures ADD COLUMN IF NOT EXISTS family_id UUID;
CREATE INDEX IF NOT EXISTS idx_mfa_failures_family ON auth.mfa_failures (user_id, family_id, failed_at);
