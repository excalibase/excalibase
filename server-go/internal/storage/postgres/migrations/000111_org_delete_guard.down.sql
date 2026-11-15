DROP TRIGGER IF EXISTS database_instances_require_org ON database_instances;
DROP FUNCTION IF EXISTS require_org_for_project();
DROP TRIGGER IF EXISTS orgs_refuse_delete_with_projects ON orgs;
DROP FUNCTION IF EXISTS refuse_org_delete_with_projects();
ALTER TABLE users DROP COLUMN IF EXISTS personal_org_issued_at;
ALTER TABLE users DROP COLUMN IF EXISTS free_orgs_deleted;
