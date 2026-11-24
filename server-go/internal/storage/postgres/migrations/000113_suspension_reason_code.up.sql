-- The rule a suspension is for (EXC-841). Nullable: rows written before this
-- have none.
ALTER TABLE abuse_suspensions ADD COLUMN IF NOT EXISTS reason_code TEXT;

-- When the suspension mail was settled: sent, or not owed (the flag was off).
-- Null means owed, and suspending again retries it. Rows that exist now were
-- never to be mailed, so they count as settled.
ALTER TABLE abuse_suspensions ADD COLUMN IF NOT EXISTS mail_sent_at TIMESTAMPTZ;
UPDATE abuse_suspensions SET mail_sent_at = COALESCE(suspended_at, NOW()) WHERE mail_sent_at IS NULL;

-- The closure mail of an org revoke (EXC-841), claimed once per (owner, org):
-- it does not depend on the owner's suspension mail, so an owner suspended
-- earlier, or with a second org revoked later, is still told.
CREATE TABLE IF NOT EXISTS abuse_org_closures (
    user_id   TEXT NOT NULL,
    org_id    TEXT NOT NULL,
    claimed_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (user_id, org_id)
);
