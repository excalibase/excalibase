-- A build sent back because the publish spool stayed full (EXC-547). requeues bounds what one push costs,
-- retry_at is when it may run again and its new place in the queue, wait_reason what the customer is told meanwhile.
ALTER TABLE site_builds ADD COLUMN requeues INTEGER NOT NULL DEFAULT 0;
ALTER TABLE site_builds ADD COLUMN retry_at TIMESTAMPTZ;
ALTER TABLE site_builds ADD COLUMN wait_reason TEXT;

DROP INDEX IF EXISTS idx_site_builds_queued;
CREATE INDEX IF NOT EXISTS idx_site_builds_queued ON site_builds((COALESCE(retry_at, created_at))) WHERE status = 'QUEUED';
