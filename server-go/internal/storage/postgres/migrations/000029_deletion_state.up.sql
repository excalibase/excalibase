-- Observed project deletion (EXC-402).
--
-- A project row now survives until every cleanup step is observed complete.
-- When a step fails the row stays in status DELETING and records which step
-- stopped and why, so the API can report the failure instead of claiming a
-- deletion it never saw, and a retry can resume from a known point.

-- deletion_delete_backups is the backup decision the deletion was started
-- with. It lives on the row, not in the request, so a retry that carries no
-- body cannot drop a purge an earlier attempt was told to perform.
ALTER TABLE database_instances
    ADD COLUMN IF NOT EXISTS deletion_step           TEXT    NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS deletion_error          TEXT    NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS deletion_delete_backups BOOLEAN NOT NULL DEFAULT FALSE;
