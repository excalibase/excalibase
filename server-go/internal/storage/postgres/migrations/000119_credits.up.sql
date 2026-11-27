-- Credits ledger (EXC-866). Money is integer micro-dollars. No foreign keys to
-- orgs or users: org hard delete maps 23503 to "has projects", and the ledger
-- outlives the org, as trust_audit outlives the account.

CREATE TABLE IF NOT EXISTS credit_accounts (
    org_id         TEXT PRIMARY KEY,
    balance_micros BIGINT NOT NULL DEFAULT 0 CHECK (balance_micros >= 0),
    version        BIGINT NOT NULL DEFAULT 0,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS credit_entries (
    id              BIGSERIAL PRIMARY KEY,
    org_id          TEXT NOT NULL,
    kind            TEXT NOT NULL CHECK (kind IN ('grant_trial', 'grant_admin', 'adjust_admin', 'usage', 'expire')),
    amount_micros   BIGINT NOT NULL,
    idempotency_key TEXT NOT NULL UNIQUE,
    period_start    TIMESTAMPTZ,
    period_end      TIMESTAMPTZ,
    actor           TEXT NOT NULL CHECK (actor <> ''),
    reason          TEXT NOT NULL DEFAULT '',
    details         JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK (
        (kind IN ('grant_trial', 'grant_admin') AND amount_micros > 0)
        OR (kind = 'adjust_admin' AND amount_micros <> 0)
        OR (kind = 'usage' AND amount_micros <= 0)
        OR (kind = 'expire' AND amount_micros < 0)
    )
);
CREATE INDEX IF NOT EXISTS idx_credit_entries_org ON credit_entries (org_id, id DESC);
CREATE INDEX IF NOT EXISTS idx_credit_entries_org_time ON credit_entries (org_id, created_at);

CREATE TABLE IF NOT EXISTS credit_lots (
    id               BIGSERIAL PRIMARY KEY,
    org_id           TEXT NOT NULL,
    source_entry_id  BIGINT NOT NULL,
    original_micros  BIGINT NOT NULL CHECK (original_micros > 0),
    remaining_micros BIGINT NOT NULL CHECK (remaining_micros >= 0),
    expires_at       TIMESTAMPTZ,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK (remaining_micros <= original_micros)
);
CREATE INDEX IF NOT EXISTS idx_credit_lots_org_open ON credit_lots (org_id, expires_at) WHERE remaining_micros > 0;

-- Every negative entry is split across lots here, so lots always add up.
CREATE TABLE IF NOT EXISTS credit_lot_debits (
    entry_id      BIGINT NOT NULL,
    lot_id        BIGINT NOT NULL,
    amount_micros BIGINT NOT NULL CHECK (amount_micros > 0),
    PRIMARY KEY (entry_id, lot_id)
);

CREATE TABLE IF NOT EXISTS credit_grant_claims (
    claim_kind        TEXT NOT NULL CHECK (claim_kind IN ('user', 'github', 'google', 'email')),
    claim_value       TEXT NOT NULL,
    user_id           TEXT NOT NULL,
    org_id            TEXT NOT NULL,
    -- NULL: the person was refused a grant (a linked identity already claimed elsewhere).
    entry_id          BIGINT,
    terms_version     TEXT NOT NULL DEFAULT '',
    terms_accepted_at TIMESTAMPTZ,
    at                TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (claim_kind, claim_value)
);
CREATE INDEX IF NOT EXISTS idx_credit_grant_claims_user ON credit_grant_claims (user_id);

CREATE TABLE IF NOT EXISTS credit_terms_acceptances (
    user_id       TEXT PRIMARY KEY,
    terms_version TEXT NOT NULL,
    accepted_at   TIMESTAMPTZ NOT NULL
);

-- Verified people who accepted the terms while the trial cap was full, first come first served.
CREATE TABLE IF NOT EXISTS credit_waitlist (
    seq       BIGSERIAL PRIMARY KEY,
    user_id   TEXT NOT NULL UNIQUE,
    queued_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- How far the usage meter has billed each org. A lot expires only once the
-- meter has passed its expiry, so every hour before it is billed to it first.
CREATE TABLE IF NOT EXISTS credit_meter_watermarks (
    org_id     TEXT PRIMARY KEY,
    watermark  TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE OR REPLACE FUNCTION credit_ledger_is_append_only() RETURNS trigger AS $$
BEGIN
    RAISE EXCEPTION '% on % is not allowed: the credit ledger is append-only', TG_OP, TG_TABLE_NAME
        USING ERRCODE = 'restrict_violation';
END;
$$ LANGUAGE plpgsql;

DO $$
DECLARE
    ledger TEXT;
BEGIN
    FOREACH ledger IN ARRAY ARRAY['credit_entries', 'credit_lot_debits', 'credit_grant_claims'] LOOP
        EXECUTE format('DROP TRIGGER IF EXISTS %I ON %I', ledger || '_no_change', ledger);
        EXECUTE format('CREATE TRIGGER %I BEFORE UPDATE OR DELETE ON %I FOR EACH ROW EXECUTE FUNCTION credit_ledger_is_append_only()', ledger || '_no_change', ledger);
        EXECUTE format('DROP TRIGGER IF EXISTS %I ON %I', ledger || '_no_truncate', ledger);
        EXECUTE format('CREATE TRIGGER %I BEFORE TRUNCATE ON %I FOR EACH STATEMENT EXECUTE FUNCTION credit_ledger_is_append_only()', ledger || '_no_truncate', ledger);
    END LOOP;
END $$;
