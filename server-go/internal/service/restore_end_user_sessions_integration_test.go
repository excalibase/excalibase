//go:build integration

package service

import (
	"context"
	"strings"
	"testing"
)

// EXC-635: a project restored from a backup keeps the source's end users but
// none of what the source issued them. The tables are excalibase-auth's as
// its Postgres migrations leave them.
const sourceEndUserAuthSQL = `
CREATE SCHEMA auth;
CREATE TABLE auth.users (
    id BIGSERIAL PRIMARY KEY, email VARCHAR(100) NOT NULL UNIQUE, password VARCHAR(255) NOT NULL,
    full_name VARCHAR(100) NOT NULL, role VARCHAR(63) NOT NULL DEFAULT 'user', allowed_roles TEXT[],
    enabled BOOLEAN NOT NULL DEFAULT true, email_verified BOOLEAN NOT NULL DEFAULT false,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(), updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(), last_login_at TIMESTAMPTZ);
CREATE TABLE auth.refresh_tokens (
    id BIGSERIAL PRIMARY KEY, token_hash CHAR(64) NOT NULL UNIQUE, family_id UUID NOT NULL,
    user_id BIGINT NOT NULL REFERENCES auth.users(id) ON DELETE CASCADE, expiry_date TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(), revoked BOOLEAN NOT NULL DEFAULT false);
CREATE TABLE auth.api_keys (
    id BIGSERIAL PRIMARY KEY, key_hash VARCHAR(64) NOT NULL UNIQUE, key_prefix VARCHAR(24) NOT NULL,
    key_type VARCHAR(16) NOT NULL CHECK (key_type IN ('publishable','secret')), name VARCHAR(100),
    created_by BIGINT REFERENCES auth.users(id) ON DELETE CASCADE, created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    last_used_at TIMESTAMPTZ, revoked_at TIMESTAMPTZ, revoked_by BIGINT REFERENCES auth.users(id) ON DELETE SET NULL);
CREATE TABLE auth.email_verification_tokens (
    id BIGSERIAL PRIMARY KEY, token_hash CHAR(64) NOT NULL UNIQUE,
    user_id BIGINT NOT NULL REFERENCES auth.users(id) ON DELETE CASCADE, expires_at TIMESTAMPTZ NOT NULL,
    consumed_at TIMESTAMPTZ, created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(), confirms_password BOOLEAN NOT NULL DEFAULT false);
CREATE TABLE auth.password_reset_tokens (
    id BIGSERIAL PRIMARY KEY, token_hash CHAR(64) NOT NULL UNIQUE,
    user_id BIGINT NOT NULL REFERENCES auth.users(id) ON DELETE CASCADE, expires_at TIMESTAMPTZ NOT NULL,
    consumed_at TIMESTAMPTZ, created_at TIMESTAMPTZ NOT NULL DEFAULT NOW());
INSERT INTO auth.users (email, password, full_name) VALUES ('ada@example.com', 'argon2-hash', 'Ada');
INSERT INTO auth.refresh_tokens (token_hash, family_id, user_id, expiry_date)
    VALUES (encode(sha256('source-refresh-token'), 'hex'), gen_random_uuid(), 1, NOW() + interval '30 days');
INSERT INTO auth.api_keys (key_hash, key_prefix, key_type) VALUES (encode(sha256('source-secret-key'), 'hex'), 'esk_source', 'secret');
INSERT INTO auth.email_verification_tokens (token_hash, user_id, expires_at) VALUES (encode(sha256('source-verify'), 'hex'), 1, NOW() + interval '1 day');
INSERT INTO auth.password_reset_tokens (token_hash, user_id, expires_at) VALUES (encode(sha256('source-reset'), 'hex'), 1, NOW() + interval '1 hour');
`

// What excalibase-auth looks each presented credential up with; every one
// must find nothing in the copy.
var sourceCredentialLookups = map[string]string{
	"refresh token": `SELECT count(*) FROM auth.refresh_tokens
		WHERE token_hash = encode(sha256('source-refresh-token'), 'hex') AND revoked = false AND expiry_date > NOW()`,
	"secret key":          `SELECT count(*) FROM auth.api_keys WHERE key_hash = encode(sha256('source-secret-key'), 'hex') AND revoked_at IS NULL`,
	"verification link":   `SELECT count(*) FROM auth.email_verification_tokens WHERE token_hash = encode(sha256('source-verify'), 'hex')`,
	"password reset link": `SELECT count(*) FROM auth.password_reset_tokens WHERE token_hash = encode(sha256('source-reset'), 'hex')`,
}

func seedSourceEndUserAuth(ctx context.Context, t *testing.T, containerID, password string) {
	t.Helper()
	if err := dockerExecPSQL(ctx, containerID, password, sourceEndUserAuthSQL); err != nil {
		t.Fatalf("seed the source's end users: %v", err)
	}
}

func assertNothingTheSourceIssuedOpensTheCopy(ctx context.Context, t *testing.T, copyID, sourceID, password string) {
	t.Helper()
	for credential, lookup := range sourceCredentialLookups {
		if got := psqlScalar(ctx, t, copyID, password, lookup); got != "0" {
			t.Errorf("the copy still accepts the source's %s (%s rows)", credential, got)
		}
		if got := psqlScalar(ctx, t, sourceID, password, lookup); got != "1" {
			t.Errorf("the source lost its own %s (%s rows)", credential, got)
		}
	}
	if got := psqlScalar(ctx, t, copyID, password, `SELECT email FROM auth.users`); got != "ada@example.com" {
		t.Errorf("the copy's end users = %q, want the source's", got)
	}
}

func psqlScalar(ctx context.Context, t *testing.T, containerID, password, query string) string {
	t.Helper()
	out, err := dockerExecPSQLQuery(ctx, containerID, password, query)
	if err != nil {
		t.Fatalf("%s: %v", query, err)
	}
	return strings.TrimSpace(out)
}
