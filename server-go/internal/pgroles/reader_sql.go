package pgroles

import "strings"

// ReaderRole is the SELECT-only login the read address names (EXC-675): a
// session held across a failover lands on a primary that takes writes, so the
// login itself must not be able to write.
const ReaderRole = "excalibase_reader"

const (
	readerShareTrigger  = "excalibase_share_schema_with_reader"
	readerShareFunction = "excalibase.share_created_schema_with_reader()"
	readerDDLTrigger    = "excalibase_reader_refuses_ddl"
	readerDDLFunction   = "excalibase.reader_refuses_ddl()"
)

// ReaderRoleSQL creates the reader if absent and lets it read every relation
// in the user schemas, now and later: default privileges for the two roles
// that create user tables (the owner and excalibase_app), and a trigger that
// shares a schema either of them creates. Its sessions start read-only and an
// event trigger refuses it any DDL, which Postgres 14 would otherwise allow
// in public through PUBLIC's CREATE.
func ReaderRoleSQL(owner, password string) string {
	return strings.NewReplacer(
		"{{READER}}", quoteLiteral(ReaderRole),
		"{{PASSWORD}}", textLiteral(password),
		"{{CREATORS}}", "ARRAY["+textLiteral(owner)+", "+quoteLiteral(appRole)+"]",
		"{{USER_SCHEMA}}", UserSchemaPredicate,
		"{{SHARE_TRIGGER}}", readerShareTrigger,
		"{{SHARE_FUNCTION}}", readerShareFunction,
		"{{DDL_TRIGGER}}", readerDDLTrigger,
		"{{DDL_FUNCTION}}", readerDDLFunction,
	).Replace(readerRoleTemplate)
}

// ReaderLockSQL leaves a reader the platform did not file unable to sign in:
// a copy restored from a project that had one still carries its password.
func ReaderLockSQL() string {
	return strings.ReplaceAll(`
DO $excalibase_reader_lock$
BEGIN
  IF to_regrole({{READER}}) IS NOT NULL THEN
    EXECUTE format('ALTER ROLE %I NOLOGIN', {{READER}});
  END IF;
END
$excalibase_reader_lock$;
`, "{{READER}}", quoteLiteral(ReaderRole))
}

const readerRoleTemplate = `
DO $excalibase_reader_role$
BEGIN
  IF to_regrole({{READER}}) IS NULL THEN
    EXECUTE format('CREATE ROLE %I WITH LOGIN PASSWORD %L', {{READER}}, {{PASSWORD}});
  END IF;
  EXECUTE format('ALTER ROLE %I WITH LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOINHERIT NOREPLICATION NOBYPASSRLS', {{READER}});
  EXECUTE format('ALTER ROLE %I SET default_transaction_read_only = on', {{READER}});
END
$excalibase_reader_role$;

DROP EVENT TRIGGER IF EXISTS {{SHARE_TRIGGER}};
DROP EVENT TRIGGER IF EXISTS {{DDL_TRIGGER}};
DROP ROUTINE IF EXISTS {{SHARE_FUNCTION}};
DROP ROUTINE IF EXISTS {{DDL_FUNCTION}};

-- Runs as whoever created the schema, so it sets that role's own defaults.
CREATE FUNCTION {{SHARE_FUNCTION}}
RETURNS event_trigger
LANGUAGE plpgsql
SECURITY INVOKER
SET search_path = pg_catalog, pg_temp
AS $fn$
DECLARE
  created record;
BEGIN
  IF NOT current_user = ANY ({{CREATORS}}) THEN
    RETURN;
  END IF;
  FOR created IN
    SELECT n.nspname FROM pg_event_trigger_ddl_commands() c
    JOIN pg_namespace n ON n.oid = c.objid
    WHERE c.object_type = 'schema' AND {{USER_SCHEMA}}
  LOOP
    EXECUTE format('GRANT USAGE ON SCHEMA %I TO %I', created.nspname, {{READER}});
    EXECUTE format('ALTER DEFAULT PRIVILEGES IN SCHEMA %I GRANT SELECT ON TABLES TO %I', created.nspname, {{READER}});
    EXECUTE format('ALTER DEFAULT PRIVILEGES IN SCHEMA %I GRANT SELECT ON SEQUENCES TO %I', created.nspname, {{READER}});
  END LOOP;
END
$fn$;
ALTER FUNCTION {{SHARE_FUNCTION}} OWNER TO CURRENT_USER;
REVOKE ALL ON FUNCTION {{SHARE_FUNCTION}} FROM PUBLIC;
CREATE EVENT TRIGGER {{SHARE_TRIGGER}} ON ddl_command_end WHEN TAG IN ('CREATE SCHEMA')
  EXECUTE FUNCTION {{SHARE_FUNCTION}};

CREATE FUNCTION {{DDL_FUNCTION}}
RETURNS event_trigger
LANGUAGE plpgsql
SECURITY INVOKER
SET search_path = pg_catalog, pg_temp
AS $fn$
BEGIN
  IF session_user = {{READER}} OR current_user = {{READER}} THEN
    RAISE EXCEPTION 'the read-only login cannot change the schema' USING ERRCODE = 'insufficient_privilege';
  END IF;
END
$fn$;
ALTER FUNCTION {{DDL_FUNCTION}} OWNER TO CURRENT_USER;
REVOKE ALL ON FUNCTION {{DDL_FUNCTION}} FROM PUBLIC;
CREATE EVENT TRIGGER {{DDL_TRIGGER}} ON ddl_command_start EXECUTE FUNCTION {{DDL_FUNCTION}};

DO $excalibase_share_reader$
DECLARE
  target record;
  creator text;
  relation record;
BEGIN
  FOR target IN SELECT n.oid, n.nspname FROM pg_namespace n WHERE {{USER_SCHEMA}} LOOP
    EXECUTE format('GRANT USAGE ON SCHEMA %I TO %I', target.nspname, {{READER}});
    FOREACH creator IN ARRAY {{CREATORS}} LOOP
      IF to_regrole(creator) IS NOT NULL THEN
        EXECUTE format('ALTER DEFAULT PRIVILEGES FOR ROLE %I IN SCHEMA %I GRANT SELECT ON TABLES TO %I', creator, target.nspname, {{READER}});
        EXECUTE format('ALTER DEFAULT PRIVILEGES FOR ROLE %I IN SCHEMA %I GRANT SELECT ON SEQUENCES TO %I', creator, target.nspname, {{READER}});
      END IF;
    END LOOP;
    FOR relation IN
      SELECT c.relname, c.relkind FROM pg_class c
      WHERE c.relnamespace = target.oid AND c.relkind IN ('r', 'p', 'v', 'm', 'f', 'S')
    LOOP
      EXECUTE format('GRANT SELECT ON %s %I.%I TO %I',
        CASE WHEN relation.relkind = 'S' THEN 'SEQUENCE' ELSE 'TABLE' END,
        target.nspname, relation.relname, {{READER}});
    END LOOP;
  END LOOP;
END
$excalibase_share_reader$;
`
