-- A restore is driven by a goroutine in one replica. Without a record of
-- which replica that is, a rolling deploy makes every restarting replica
-- fail restores its peers are actively running: the caller is told FAILED
-- while the restore is still going, retries, and ends up with two.
--
-- owner names the process driving the job; heartbeat_at is refreshed while
-- it is alive. A sweep fails only jobs it does not own whose heartbeat has
-- gone stale, and every progress write is conditional on both, so a driver
-- that lost its job cannot write over the outcome the sweep recorded.
ALTER TABLE restore_jobs ADD COLUMN IF NOT EXISTS owner TEXT NOT NULL DEFAULT '';
ALTER TABLE restore_jobs ADD COLUMN IF NOT EXISTS heartbeat_at TIMESTAMPTZ NOT NULL DEFAULT NOW();

-- The sweep reads running jobs by liveness.
CREATE INDEX IF NOT EXISTS idx_restore_jobs_heartbeat ON restore_jobs(status, heartbeat_at);
