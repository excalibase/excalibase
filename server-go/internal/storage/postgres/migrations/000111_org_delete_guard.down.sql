DROP TRIGGER IF EXISTS orgs_refuse_delete_with_projects ON orgs;
DROP FUNCTION IF EXISTS refuse_org_delete_with_projects();
ALTER TABLE users DROP COLUMN IF EXISTS free_orgs_deleted;
