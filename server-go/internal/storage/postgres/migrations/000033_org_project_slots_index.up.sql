-- Every project creation counts one organisation's slot-holding projects
-- (org_id plus a status filter) while holding that organisation's advisory
-- lock, so the count sits directly in the admission path of every provision
-- and every restore. Numbered 000033 to leave 000032 for the pause-attempt
-- columns landing on another branch.
CREATE INDEX IF NOT EXISTS idx_database_instances_org_status
    ON database_instances (org_id, status);
