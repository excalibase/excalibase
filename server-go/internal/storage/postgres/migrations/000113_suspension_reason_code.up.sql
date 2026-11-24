-- The rule a suspension is for (EXC-841). Nullable: rows written before this
-- have none.
ALTER TABLE abuse_suspensions ADD COLUMN IF NOT EXISTS reason_code TEXT;

-- When the account holder was told. A suspension whose mail has not gone is
-- retried by suspending again; rows that exist now were never to be mailed, so
-- they count as done.
ALTER TABLE abuse_suspensions ADD COLUMN IF NOT EXISTS mail_sent_at TIMESTAMPTZ;
UPDATE abuse_suspensions SET mail_sent_at = COALESCE(suspended_at, NOW()) WHERE mail_sent_at IS NULL;
