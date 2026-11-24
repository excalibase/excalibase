-- The rule a suspension is for (EXC-841). Nullable: rows written before this
-- have none.
ALTER TABLE abuse_suspensions ADD COLUMN IF NOT EXISTS reason_code TEXT;

-- When the suspension mail was settled: sent, or not owed (the flag was off).
-- Null means owed, and suspending again retries it. Rows that exist now were
-- never to be mailed, so they count as settled.
ALTER TABLE abuse_suspensions ADD COLUMN IF NOT EXISTS mail_sent_at TIMESTAMPTZ;
UPDATE abuse_suspensions SET mail_sent_at = COALESCE(suspended_at, NOW()) WHERE mail_sent_at IS NULL;
