-- The admin "needs attention" card pages suspensions by (suspended_at, user_id),
-- newest first (EXC-898); this index serves that order and its keyset seek.
CREATE INDEX IF NOT EXISTS idx_abuse_suspensions_at_user ON abuse_suspensions (suspended_at DESC, user_id DESC);
