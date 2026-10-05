-- E-mail addresses match ignoring case (EXC-513): one human account and one
-- open invite per address. Existing case-duplicates stop the migration so an
-- operator decides which account stays; nothing is merged or dropped here.
DO $$
DECLARE
    duplicates TEXT;
BEGIN
    SELECT string_agg(address, ', ') INTO duplicates FROM (
        SELECT lower(email) AS address FROM users
        WHERE kind = 'human' GROUP BY lower(email) HAVING count(*) > 1
    ) d;
    IF duplicates IS NOT NULL THEN
        RAISE EXCEPTION 'human accounts whose e-mail addresses differ only by case: %; change or remove all but one of each, then rerun the migration', duplicates;
    END IF;

    SELECT string_agg(org_id || ' ' || address, ', ') INTO duplicates FROM (
        SELECT org_id, lower(email) AS address FROM pending_invites
        GROUP BY org_id, lower(email) HAVING count(*) > 1
    ) d;
    IF duplicates IS NOT NULL THEN
        RAISE EXCEPTION 'pending invites whose e-mail addresses differ only by case: %; delete all but one of each, then rerun the migration', duplicates;
    END IF;
END $$;

CREATE UNIQUE INDEX IF NOT EXISTS idx_users_human_email_lower ON users (lower(email)) WHERE kind = 'human';

ALTER TABLE pending_invites DROP CONSTRAINT IF EXISTS pending_invites_org_id_email_key;
CREATE UNIQUE INDEX IF NOT EXISTS idx_pending_invites_org_email_lower ON pending_invites (org_id, lower(email));
