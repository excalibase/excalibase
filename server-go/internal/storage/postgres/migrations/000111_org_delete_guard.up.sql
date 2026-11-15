-- EXC-760: the free allowance belongs to the account, and an org with projects
-- cannot be deleted out from under them.
-- Free orgs the account deleted. They still count against the allowance.
ALTER TABLE users ADD COLUMN IF NOT EXISTS free_orgs_deleted INT NOT NULL DEFAULT 0;

-- database_instances.org_id has no foreign key (rows predate orgs and tests
-- seed projects for orgs that do not exist), so the guard is a trigger: it
-- stops a delete, never an insert.
CREATE OR REPLACE FUNCTION refuse_org_delete_with_projects() RETURNS trigger AS $$
BEGIN
    IF EXISTS (SELECT 1 FROM database_instances WHERE org_id = OLD.id) THEN
        RAISE EXCEPTION 'org % still has projects', OLD.id USING ERRCODE = '23503';
    END IF;
    RETURN OLD;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS orgs_refuse_delete_with_projects ON orgs;
CREATE TRIGGER orgs_refuse_delete_with_projects
    BEFORE DELETE ON orgs
    FOR EACH ROW EXECUTE FUNCTION refuse_org_delete_with_projects();
