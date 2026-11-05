package pgroles

import (
	"regexp"
	"strings"
)

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

// hbaDatabaseName is a database name pg_hba reads as written, unquoted.
var hbaDatabaseName = regexp.MustCompile(`^[a-z_][a-z0-9_]{0,62}$`)

// ReaderHBA keeps the reader to the project's database: the DDL trigger and
// the large-object revoke live only there. connType is the line type the
// server's other password logins use. A name pg_hba cannot take as written
// leaves the reader rejected everywhere.
func ReaderHBA(connType, database string) []string {
	reject := "host all " + ReaderRole + " all reject"
	if !hbaDatabaseName.MatchString(database) {
		return []string{reject}
	}
	return []string{connType + " " + database + " " + ReaderRole + " all scram-sha-256", reject}
}

// largeObjectCreators write a large object whatever the transaction mode.
// Server-side lo_import is already superuser-only and stays so.
var largeObjectCreators = []string{
	"pg_catalog.lo_creat(integer)",
	"pg_catalog.lo_create(oid)",
	"pg_catalog.lo_from_bytea(oid, bytea)",
}

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
		"{{OWNER}}", textLiteral(owner),
		"{{APP}}", quoteLiteral(appRole),
	).Replace(readerRoleTemplate + largeObjectSQL())
}

// largeObjectSQL takes large-object creation from PUBLIC, which the reader
// would otherwise reach with a read-write transaction, and gives it back to the
// owner (who may pass it on to its own roles) and excalibase_app.
func largeObjectSQL() string {
	var b strings.Builder
	for _, function := range largeObjectCreators {
		b.WriteString("REVOKE EXECUTE ON FUNCTION " + function + " FROM PUBLIC;\n")
	}
	functions := strings.Join(largeObjectCreators, ", ")
	b.WriteString(`DO $excalibase_large_objects$
BEGIN
  IF to_regrole({{OWNER}}) IS NOT NULL THEN
    EXECUTE format('GRANT EXECUTE ON FUNCTION ` + functions + ` TO %I WITH GRANT OPTION', {{OWNER}});
  END IF;
  IF to_regrole({{APP}}) IS NOT NULL THEN
    EXECUTE format('GRANT EXECUTE ON FUNCTION ` + functions + ` TO %I', {{APP}});
  END IF;
END
$excalibase_large_objects$;
`)
	return b.String()
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
