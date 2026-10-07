-- A restore either copies a project into a new one or replaces the project's
-- own database (in_place, EXC-568). The mode decides which running restores
-- a new one conflicts with.
ALTER TABLE restore_jobs ADD COLUMN IF NOT EXISTS mode TEXT NOT NULL DEFAULT 'new_project';

-- At most one in-place restore per project runs at a time; StartRestoreJob
-- checks the wider conflicts, this index is the backstop.
CREATE UNIQUE INDEX IF NOT EXISTS idx_restore_jobs_one_in_place
    ON restore_jobs(source_project_id) WHERE status = 'RUNNING' AND mode = 'in_place';
