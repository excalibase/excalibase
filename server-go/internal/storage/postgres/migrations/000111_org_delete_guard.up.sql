-- EXC-760: the free allowance belongs to the account, and an org with projects
-- cannot be deleted out from under them.

-- Free orgs the account deleted. They still count against the free-org cap.
ALTER TABLE users ADD COLUMN IF NOT EXISTS free_orgs_deleted INT NOT NULL DEFAULT 0;

-- When the account was first given an org, of any plan. Sign-in never issues a
-- personal org to an account that has one on record, so deleting or revoking
-- the org (whatever its tier) does not hand back a new one.
ALTER TABLE users ADD COLUMN IF NOT EXISTS personal_org_issued_at TIMESTAMPTZ;

UPDATE users SET personal_org_issued_at = NOW()
WHERE personal_org_issued_at IS NULL AND EXISTS (SELECT 1 FROM orgs WHERE orgs.owner_id = users.id);

-- database_instances.org_id has no foreign key (rows predate orgs), so the
-- guards are triggers. Both run under the org's project-limit advisory lock
-- (the application takes it on create and on delete), so the check and the
-- write cannot interleave.
CREATE OR REPLACE FUNCTION refuse_org_delete_with_projects() RETURNS trigger AS $$
BEGIN
    IF EXISTS (SELECT 1 FROM database_instances WHERE org_id = OLD.id)
       OR EXISTS (SELECT 1 FROM restore_jobs
                  WHERE org_id = OLD.id AND status = 'RUNNING' AND mode = 'new_project') THEN
        RAISE EXCEPTION 'org % still has projects', OLD.id USING ERRCODE = '23503';
    END IF;
    RETURN OLD;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS orgs_refuse_delete_with_projects ON orgs;
CREATE TRIGGER orgs_refuse_delete_with_projects
    BEFORE DELETE ON orgs
    FOR EACH ROW EXECUTE FUNCTION refuse_org_delete_with_projects();

-- A project belongs to an org that exists. An empty org_id stays allowed for
-- rows that carry none.
CREATE OR REPLACE FUNCTION require_org_for_project() RETURNS trigger AS $$
BEGIN
    IF NEW.org_id <> '' AND NOT EXISTS (SELECT 1 FROM orgs WHERE id = NEW.org_id) THEN
        RAISE EXCEPTION 'org % does not exist', NEW.org_id USING ERRCODE = '23503';
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS database_instances_require_org ON database_instances;
CREATE TRIGGER database_instances_require_org
    BEFORE INSERT ON database_instances
    FOR EACH ROW EXECUTE FUNCTION require_org_for_project();
