-- Usernames match ignoring case (EXC-555): Alice and alice would read as the
-- same person. Existing case-duplicates stop the migration so an operator
-- decides which account keeps the name; nothing is renamed or dropped here.
DO $$
DECLARE
    duplicates TEXT;
BEGIN
    SELECT string_agg(name, ', ') INTO duplicates FROM (
        SELECT lower(username) AS name FROM users
        GROUP BY lower(username) HAVING count(*) > 1
    ) d;
    IF duplicates IS NOT NULL THEN
        RAISE EXCEPTION 'accounts whose usernames differ only by case: %; rename or remove all but one of each, then rerun the migration', duplicates;
    END IF;
END $$;

CREATE UNIQUE INDEX IF NOT EXISTS idx_users_username_lower ON users (lower(username));
