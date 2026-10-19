DROP INDEX IF EXISTS idx_restore_jobs_org_slot_holds;
ALTER TABLE restore_jobs DROP COLUMN IF EXISTS org_id;
