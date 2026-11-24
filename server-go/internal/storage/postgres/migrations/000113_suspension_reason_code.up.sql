-- The rule a suspension is for (EXC-841). Nullable: rows written before this
-- have none, and nothing mails them.
ALTER TABLE abuse_suspensions ADD COLUMN IF NOT EXISTS reason_code TEXT;
