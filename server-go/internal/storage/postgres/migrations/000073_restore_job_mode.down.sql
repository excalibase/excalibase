DROP INDEX IF EXISTS idx_restore_jobs_one_in_place;
ALTER TABLE restore_jobs DROP COLUMN IF EXISTS mode;
