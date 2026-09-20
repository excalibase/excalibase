-- A pause that fails leaves the project in PAUSING, and only another pause
-- can move it. The idle sweep retries those, but a pause that fails every
-- time — invalid backup credentials, say — would file a Backup CR on every
-- tick, and a counter held in the scheduler is lost on every restart and
-- every leader change, which is exactly when the storm restarts.
--
-- Holding the count and the last attempt on the project row makes the
-- backoff survive both: whichever replica picks the project up next reads
-- how far the backoff had already grown.
ALTER TABLE database_instances ADD COLUMN IF NOT EXISTS pause_attempts INTEGER NOT NULL DEFAULT 0;
ALTER TABLE database_instances ADD COLUMN IF NOT EXISTS pause_last_attempt_at TIMESTAMPTZ;

-- A pause that fails after its pre-pause backup used to take a whole new
-- backup on every retry. The backup belongs to the pause episode, not to the
-- attempt: recording which one was taken lets a retry reuse it, and lets a
-- retry after a timed-out wait observe the backup that is STILL RUNNING
-- instead of filing another one beside it. Cleared when the project settles.
ALTER TABLE database_instances ADD COLUMN IF NOT EXISTS pause_backup_id TEXT NOT NULL DEFAULT '';
ALTER TABLE database_instances ADD COLUMN IF NOT EXISTS pause_backup_at TIMESTAMPTZ;
