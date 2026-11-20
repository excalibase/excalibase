DROP INDEX IF EXISTS idx_site_builds_queued;
CREATE INDEX IF NOT EXISTS idx_site_builds_queued ON site_builds(created_at) WHERE status = 'QUEUED';

ALTER TABLE site_builds DROP COLUMN IF EXISTS wait_reason;
ALTER TABLE site_builds DROP COLUMN IF EXISTS retry_at;
ALTER TABLE site_builds DROP COLUMN IF EXISTS requeues;
