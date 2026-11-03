-- A restore into a new project holds its organisation's project slot from
-- submission until the copy is registered (EXC-639). The organisation is
-- recorded on the job, so the hold does not depend on the source row.
ALTER TABLE restore_jobs ADD COLUMN IF NOT EXISTS org_id TEXT NOT NULL DEFAULT '';

CREATE INDEX IF NOT EXISTS idx_restore_jobs_org_slot_holds
    ON restore_jobs(org_id) WHERE status = 'RUNNING' AND mode = 'new_project';
