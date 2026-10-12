package pgroles

import "strings"

// UserSchemaPredicate decides, over pg_namespace n, which schemas hold user
// objects rather than the platform's or Postgres' own.
const UserSchemaPredicate = `n.nspname NOT IN ('auth', 'excalibase', 'information_schema')
    AND NOT starts_with(n.nspname, 'pg_')
    AND NOT starts_with(n.nspname, 'excalibase_')`

const (
	ShareWithOwnerSignature     = "excalibase.share_with_owner(oid)"
	shareCreatedSchemaSignature = "excalibase.share_created_schema()"
	ownerAccessTrigger          = "excalibase_share_created_schema"
)

// OwnerAccessSQL lets the project's own login use what migrations and Studio
// create. Those run as excalibase_app, which keeps ownership; the owner gets
// DML on its user schemas' tables and their sequences, never membership in the
// platform role, and nothing in the reserved schemas. Public is shared at
// registration, a schema excalibase_app creates later by an event trigger.
// Both functions run with the caller's rights and are dropped first, as any
// routine kind, since excalibase_app can create objects in the excalibase schema.
func OwnerAccessSQL(owner string) string {
	replacer := strings.NewReplacer(
		"{{OWNER}}", textLiteral(owner),
		"{{APP_ROLE}}", quoteLiteral(appRole),
		"{{USER_SCHEMA}}", UserSchemaPredicate,
		"{{SHARE}}", ShareWithOwnerSignature,
		"{{SHARE_CREATED}}", shareCreatedSchemaSignature,
		"{{TRIGGER}}", ownerAccessTrigger,
	)
	return replacer.Replace(ownerAccessTemplate)
}

const ownerAccessTemplate = `
DROP EVENT TRIGGER IF EXISTS {{TRIGGER}};
DROP ROUTINE IF EXISTS {{SHARE_CREATED}};
DROP ROUTINE IF EXISTS {{SHARE}};
CREATE FUNCTION excalibase.share_with_owner(target_schema oid)
RETURNS void
LANGUAGE plpgsql
SECURITY INVOKER
SET search_path = pg_catalog, pg_temp
AS $fn$
DECLARE
  owner_role CONSTANT text := {{OWNER}};
  platform_role CONSTANT text := {{APP_ROLE}};
  -- Never TRIGGER: a trigger runs as whoever writes the row, so one the owner
  -- attached would run its code as the platform role on every API write.
  table_privileges CONSTANT text := 'SELECT, INSERT, UPDATE, DELETE, TRUNCATE';
  schema_name text;
  relation record;
BEGIN
  SELECT n.nspname INTO schema_name FROM pg_namespace n
  WHERE n.oid = target_schema
    AND (n.nspname = 'public' OR n.nspowner = to_regrole(platform_role))
    AND {{USER_SCHEMA}};
  IF schema_name IS NULL THEN
    RETURN;
  END IF;
  EXECUTE format('GRANT USAGE ON SCHEMA %I TO %I', schema_name, owner_role);
  EXECUTE format('ALTER DEFAULT PRIVILEGES FOR ROLE %I IN SCHEMA %I GRANT ' || table_privileges || ' ON TABLES TO %I', platform_role, schema_name, owner_role);
  EXECUTE format('ALTER DEFAULT PRIVILEGES FOR ROLE %I IN SCHEMA %I GRANT ALL ON SEQUENCES TO %I', platform_role, schema_name, owner_role);
  FOR relation IN
    SELECT c.relname, c.relkind FROM pg_class c
    WHERE c.relnamespace = target_schema AND c.relowner = to_regrole(platform_role)
      AND c.relkind IN ('r', 'p', 'v', 'm', 'f', 'S')
  LOOP
    IF relation.relkind = 'S' THEN
      EXECUTE format('GRANT ALL ON SEQUENCE %I.%I TO %I', schema_name, relation.relname, owner_role);
    ELSE
      EXECUTE format('GRANT ' || table_privileges || ' ON TABLE %I.%I TO %I', schema_name, relation.relname, owner_role);
    END IF;
  END LOOP;
END
$fn$;
ALTER FUNCTION {{SHARE}} OWNER TO CURRENT_USER;
REVOKE ALL ON FUNCTION {{SHARE}} FROM PUBLIC;
DO $excalibase_share_grant$
BEGIN
  EXECUTE format('GRANT EXECUTE ON FUNCTION {{SHARE}} TO %I', {{APP_ROLE}});
END
$excalibase_share_grant$;

CREATE FUNCTION excalibase.share_created_schema()
RETURNS event_trigger
LANGUAGE plpgsql
SECURITY INVOKER
SET search_path = pg_catalog, pg_temp
AS $fn$
DECLARE
  platform_role CONSTANT text := {{APP_ROLE}};
BEGIN
  IF current_user <> platform_role THEN
    RETURN;
  END IF;
  PERFORM excalibase.share_with_owner(c.objid)
  FROM pg_event_trigger_ddl_commands() c
  WHERE c.object_type = 'schema';
END
$fn$;
ALTER FUNCTION {{SHARE_CREATED}} OWNER TO CURRENT_USER;
REVOKE ALL ON FUNCTION {{SHARE_CREATED}} FROM PUBLIC;
CREATE EVENT TRIGGER {{TRIGGER}} ON ddl_command_end WHEN TAG IN ('CREATE SCHEMA')
  EXECUTE FUNCTION {{SHARE_CREATED}};

DO $excalibase_share_existing$
BEGIN
  PERFORM excalibase.share_with_owner(n.oid) FROM pg_namespace n;
END
$excalibase_share_existing$;
`
