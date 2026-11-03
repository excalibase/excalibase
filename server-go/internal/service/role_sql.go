package service

import (
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/excalibase/provisioning-poc/internal/schema"
)

// RolePassword pairs a database role with the password it must end up with.
type RolePassword struct {
	Role     string
	Password string
}

// BuildProjectRoleResetSQL emits one ALTER ROLE per entry. A cluster restored
// from a backup already carries the source project's roles — with the source's
// passwords — so BuildProjectRoleSQL's CREATE-if-absent blocks are no-ops
// there and the freshly generated passwords the platform files in vault would
// never reach the database. Running this afterwards makes "what vault holds"
// and "what the database accepts" the same thing again.
//
// Pure, like BuildProjectRoleSQL: deterministic by input, no side effects.
func BuildProjectRoleResetSQL(roles []RolePassword) string {
	var b strings.Builder
	for _, r := range roles {
		if r.Role == "" || r.Password == "" {
			continue
		}
		b.WriteString("\n")
		b.WriteString(alterRolePasswordSQL(r.Role, r.Password))
	}
	return b.String()
}

// sqlTextLiteral renders s as an expression Postgres decodes itself. Only hex
// digits reach the statement, so no quoting context (dollar quotes,
// standard_conforming_strings=off) can be broken out of.
func sqlTextLiteral(s string) string {
	return "convert_from(decode('" + hex.EncodeToString([]byte(s)) + "', 'hex'), 'UTF8')"
}

// alterRolePasswordSQL sets a role's password with the quoting done by
// Postgres' format(), never by string interpolation.
func alterRolePasswordSQL(role, password string) string {
	return fmt.Sprintf("DO $$ BEGIN EXECUTE format('ALTER ROLE %%I WITH LOGIN PASSWORD %%L', %s, %s); END $$;",
		sqlTextLiteral(role), sqlTextLiteral(password))
}

// reservedSchemaRebuildSQL drops the excalibase schema when anything in it, or
// the schema itself, belongs to a role that is not a superuser. Before EXC-623
// excalibase_app could create there, and an object it planted (a SECURITY
// DEFINER function, a trigger on a table it owned, an overload) would outlive
// a revoke. The rest of the registration SQL recreates every platform object,
// so a clean schema is never dropped and its scheduler rows stay.
//
// A table or other object the drop takes with it is lost, rows included: on a
// project registered before EXC-623 that is the scheduler's tables, which
// excalibase_app owned, so pending tasks go and cron rows come back on the
// function's next deploy. Copying the rows out first would read tables the
// app role shaped, as the superuser. No project runs on that state.
//
// Default privileges a role set in the schema are not objects in it and do not
// count: the lockdown clears them instead of costing the scheduler its rows.
const reservedSchemaRebuildSQL = `
DO $excalibase_reserved_rebuild$
BEGIN
  IF EXISTS (
    SELECT 1 FROM pg_namespace n JOIN pg_roles r ON r.oid = n.nspowner
    WHERE n.nspname = 'excalibase' AND NOT r.rolsuper
  ) OR EXISTS (
    SELECT 1 FROM pg_namespace n
    JOIN pg_depend d ON d.refclassid = 'pg_catalog.pg_namespace'::regclass AND d.refobjid = n.oid
    JOIN pg_shdepend s ON s.classid = d.classid AND s.objid = d.objid AND s.deptype = 'o'
      AND s.dbid = (SELECT oid FROM pg_database WHERE datname = current_database())
    JOIN pg_roles r ON r.oid = s.refobjid
    WHERE n.nspname = 'excalibase' AND NOT r.rolsuper
      AND d.classid <> 'pg_catalog.pg_default_acl'::regclass
  ) OR EXISTS (
    SELECT 1 FROM pg_trigger t
    JOIN pg_class c ON c.oid = t.tgrelid
    JOIN pg_namespace n ON n.oid = c.relnamespace
    WHERE n.nspname = 'excalibase' AND NOT t.tgisinternal
  ) THEN
    RAISE WARNING 'the excalibase schema held objects a non-superuser owned; it is dropped and rebuilt';
    DROP SCHEMA excalibase CASCADE;
  END IF;
END
$excalibase_reserved_rebuild$;
`

// reservedSchemaLockdownSQL leaves the excalibase schema the superuser's: it
// takes the schema, takes CREATE from every other role and leaves the app role
// USAGE and nothing on its tables. It stands alone so the reconcile can apply
// it before anything a customer can make fail (EXC-623). appRoleName is the
// bare role name; the SQL quotes it.
func reservedSchemaLockdownSQL(appRoleName string) string {
	return strings.ReplaceAll(reservedSchemaLockdownTemplate, "{{APP}}", sqlTextLiteral(appRoleName))
}

const reservedSchemaLockdownTemplate = `
-- Reserved schema for platform bookkeeping, outside public, which the
-- generated APIs expose. The superuser alone creates and owns what is in it
-- (EXC-623): customer SQL runs as excalibase_app, which only reaches the
-- guarded functions here, so no role but the superuser may CREATE. Default
-- privileges another role set in the schema are cleared: they could only
-- apply to objects that role can no longer create there.
CREATE SCHEMA IF NOT EXISTS excalibase;
ALTER SCHEMA excalibase OWNER TO CURRENT_USER;
DO $excalibase_reserved_create$
DECLARE
  grantee text;
  acl record;
BEGIN
  FOR grantee IN
    SELECT CASE WHEN a.grantee = 0 THEN 'PUBLIC' ELSE quote_ident(r.rolname) END
    FROM pg_namespace n
    CROSS JOIN LATERAL aclexplode(n.nspacl) a
    LEFT JOIN pg_roles r ON r.oid = a.grantee
    WHERE n.nspname = 'excalibase' AND a.privilege_type = 'CREATE' AND a.grantee <> n.nspowner
  LOOP
    EXECUTE 'REVOKE CREATE ON SCHEMA excalibase FROM ' || grantee || ' CASCADE';
  END LOOP;
  FOR acl IN
    SELECT DISTINCT quote_ident(owner.rolname) AS owner_name,
      CASE d.defaclobjtype WHEN 'r' THEN 'TABLES' WHEN 'S' THEN 'SEQUENCES'
        WHEN 'f' THEN 'FUNCTIONS' WHEN 'T' THEN 'TYPES' END AS kind,
      CASE WHEN a.grantee = 0 THEN 'PUBLIC' ELSE quote_ident(g.rolname) END AS grantee_name
    FROM pg_default_acl d
    JOIN pg_namespace n ON n.oid = d.defaclnamespace
    JOIN pg_roles owner ON owner.oid = d.defaclrole
    CROSS JOIN LATERAL aclexplode(d.defaclacl) a
    LEFT JOIN pg_roles g ON g.oid = a.grantee
    WHERE n.nspname = 'excalibase' AND NOT owner.rolsuper AND d.defaclobjtype IN ('r', 'S', 'f', 'T')
  LOOP
    EXECUTE 'ALTER DEFAULT PRIVILEGES FOR ROLE ' || acl.owner_name || ' IN SCHEMA excalibase REVOKE ALL ON '
      || acl.kind || ' FROM ' || acl.grantee_name || ' CASCADE';
  END LOOP;
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = {{APP}}) THEN
    EXECUTE format('REVOKE ALL ON ALL TABLES IN SCHEMA excalibase FROM %I', {{APP}});
    EXECUTE format('REVOKE ALL ON ALL SEQUENCES IN SCHEMA excalibase FROM %I', {{APP}});
    EXECUTE format('GRANT USAGE ON SCHEMA excalibase TO %I', {{APP}});
  ELSE
    RAISE NOTICE 'excalibase_app does not exist yet; registration grants it USAGE';
  END IF;
  -- Take USAGE on the schema's own types from PUBLIC so no customer can type
  -- an object elsewhere on one, which would make that object a dependent the
  -- next rebuild must lock. DML on the platform tables does not need it.
  FOR acl IN
    SELECT quote_ident(t.typname) AS name
    FROM pg_type t JOIN pg_namespace n ON n.oid = t.typnamespace
    WHERE n.nspname = 'excalibase' AND t.typtype IN ('c', 'e', 'd', 'r')
      AND has_type_privilege('public', t.oid, 'USAGE')
  LOOP
    EXECUTE 'REVOKE USAGE ON TYPE excalibase.' || acl.name || ' FROM PUBLIC';
  END LOOP;
END
$excalibase_reserved_create$;
`

// reservedSchemaGuardSQL refuses a non-superuser's CREATE EXTENSION or ALTER
// EXTENSION that puts anything in a reserved schema. Creating a trusted
// extension checks CREATE on the database, not on the schema it names, and
// its script runs as the superuser, so a revoke alone does not keep
// excalibase_app from installing one into excalibase or auth.
const reservedSchemaGuardSQL = `
DROP EVENT TRIGGER IF EXISTS excalibase_guard_reserved_schemas;
DROP ROUTINE IF EXISTS excalibase.guard_reserved_schemas();
CREATE FUNCTION excalibase.guard_reserved_schemas()
RETURNS event_trigger
LANGUAGE plpgsql
SECURITY INVOKER
SET search_path = pg_catalog, pg_temp
AS $fn$
DECLARE
  placed text;
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = current_user AND rolsuper) THEN
    RETURN;
  END IF;
  SELECT n.nspname INTO placed
  FROM pg_event_trigger_ddl_commands() d
  LEFT JOIN pg_extension e ON d.classid = 'pg_catalog.pg_extension'::regclass AND e.oid = d.objid
  JOIN pg_namespace n ON n.nspname = d.schema_name OR n.oid = e.extnamespace
  WHERE n.nspname IN ('auth', 'excalibase') OR starts_with(n.nspname, 'excalibase_')
  LIMIT 1;
  IF placed IS NOT NULL THEN
    RAISE EXCEPTION 'permission denied for schema %', placed USING ERRCODE = 'insufficient_privilege';
  END IF;
END
$fn$;
ALTER FUNCTION excalibase.guard_reserved_schemas() OWNER TO CURRENT_USER;
REVOKE ALL ON FUNCTION excalibase.guard_reserved_schemas() FROM PUBLIC;
CREATE EVENT TRIGGER excalibase_guard_reserved_schemas ON ddl_command_end
  WHEN TAG IN ('CREATE EXTENSION', 'ALTER EXTENSION')
  EXECUTE FUNCTION excalibase.guard_reserved_schemas();
`

// authSchemaRevokeSQL takes from appRole whatever it holds in the auth schema:
// its own grants, what it gets through PUBLIC or membership of authRole, and
// the default privileges that would hand it the auth service's next table. A
// superuser's REVOKE acts as each object's owner, so grants auth_admin made go
// too.
func authSchemaRevokeSQL(appRole, authRole string) string {
	return fmt.Sprintf(`REVOKE ALL ON ALL TABLES IN SCHEMA auth FROM %[1]s, PUBLIC;
REVOKE ALL ON ALL SEQUENCES IN SCHEMA auth FROM %[1]s, PUBLIC;
REVOKE ALL ON ALL FUNCTIONS IN SCHEMA auth FROM %[1]s;
REVOKE ALL ON SCHEMA auth FROM %[1]s, PUBLIC;
REVOKE %[2]s FROM %[1]s;
ALTER DEFAULT PRIVILEGES IN SCHEMA auth REVOKE ALL ON TABLES FROM %[1]s;
-- The auth service creates its tables as auth_admin after registration.
ALTER DEFAULT PRIVILEGES FOR ROLE %[2]s IN SCHEMA auth REVOKE ALL ON TABLES FROM %[1]s;
ALTER DEFAULT PRIVILEGES FOR ROLE %[2]s IN SCHEMA auth REVOKE ALL ON SEQUENCES FROM %[1]s;`, appRole, authRole)
}

// BuildProjectRoleSQL produces the role-creation SQL run as the postgres
// superuser at provisioning time. Output:
//
//   - auth_admin   — owns the auth schema (used by excalibase-auth)
//   - excalibase_app — DML on public, CREATE on the database. NO REPLICATION.
//     Nothing in auth: only auth_admin reaches the end users' tables, and the
//     grants an older registration or backup gave are revoked (EXC-628).
//     Customer-written SQL runs as this role, so it holds no write, CREATE or
//     ownership in the excalibase schema (EXC-623); it only calls the guarded
//     functions there.
//   - cdc_watcher  — REPLICATION attribute, used by the watcher daemon
//     to consume the logical slot. Writes only its own excalibase_cdc schema.
//   - empty publication, owned by the superuser. excalibase_app changes its
//     membership only through excalibase.set_realtime_table, which publishes
//     user tables whoever owns them and nothing else. Name is configurable
//     because the watcher daemon's config is the source of truth — both
//     ends must agree.
//
// The function is pure (no side effects, deterministic by input) so unit
// tests can assert on the produced statement set without a live DB.
func BuildProjectRoleSQL(authPass, appPass, watcherPass, dbName, publicationName string) string {
	if publicationName == "" {
		publicationName = "cdc_watcher_pub"
	}
	authRole := schema.QuoteIdent("auth_admin")
	appRole := schema.QuoteIdent("excalibase_app")
	watcherRole := schema.QuoteIdent("cdc_watcher")
	pubIdent := schema.QuoteIdent(publicationName)
	pubName := sqlTextLiteral(publicationName)
	safeAuthPass := sqlTextLiteral(authPass)
	safeAppPass := sqlTextLiteral(appPass)
	safeWatcherPass := sqlTextLiteral(watcherPass)
	dbIdent := schema.QuoteIdent(dbName)

	return reservedSchemaRebuildSQL + fmt.Sprintf(`
CREATE SCHEMA IF NOT EXISTS auth;

DO $$ BEGIN
  IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'auth_admin') THEN
    EXECUTE format('CREATE ROLE %s WITH LOGIN PASSWORD %%L', %s);
  END IF;
END $$;
GRANT CREATE ON DATABASE %s TO %s;
GRANT ALL ON SCHEMA auth TO %s;
ALTER DEFAULT PRIVILEGES IN SCHEMA auth GRANT ALL ON TABLES TO %s;

DO $$ BEGIN
  IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'excalibase_app') THEN
    EXECUTE format('CREATE ROLE %s WITH LOGIN PASSWORD %%L', %s);
  END IF;
END $$;
-- CREATE on the database lets a migration create a schema (EXC-599).
GRANT CREATE ON DATABASE %s TO %s;
GRANT ALL ON SCHEMA public TO %s;
GRANT ALL ON ALL TABLES IN SCHEMA public TO %s;
GRANT ALL ON ALL SEQUENCES IN SCHEMA public TO %s;
ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT ALL ON TABLES TO %s;
ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT ALL ON SEQUENCES TO %s;
%s


%s
-- cdc_watcher: dedicated role with REPLICATION attribute used ONLY by the
-- watcher daemon to consume the logical slot. No DML grants. Separating
-- this from excalibase_app means a leaked excalibase_app credential cannot
-- dump raw WAL via test_decoding (which would bypass RLS and expose auth).
DO $$ BEGIN
  IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'cdc_watcher') THEN
    EXECUTE format('CREATE ROLE %s WITH LOGIN REPLICATION PASSWORD %%L', %s);
  END IF;
END $$;

-- The watcher keeps its slot owner registry in a schema of its own, found
-- through its search_path, so it needs no write access to public.
CREATE SCHEMA IF NOT EXISTS excalibase_cdc;
REVOKE ALL ON SCHEMA excalibase_cdc FROM PUBLIC;
GRANT USAGE, CREATE ON SCHEMA excalibase_cdc TO %s;
ALTER ROLE %s IN DATABASE %s SET search_path = excalibase_cdc;

-- Empty publication; tables are added on demand from the realtime API. The
-- watcher daemon's config is the source of truth for its name.
DO $$ BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_publication WHERE pubname = %s) THEN
    EXECUTE format('CREATE PUBLICATION %%I', %s);
  END IF;
END $$;
ALTER PUBLICATION %s OWNER TO CURRENT_USER;

-- ALTER PUBLICATION ADD TABLE needs ownership of the publication and of the
-- table, and user tables belong to the project owner role. Rather than let
-- excalibase_app act as that owner, this function does the one thing needed:
-- toggle a user table in this publication. Platform and system schemas stay out.
-- A published table logs complete old rows (REPLICA IDENTITY FULL): realtime
-- judges each delete against the subscriber's permission, which reads columns
-- outside the key. Unpublishing restores the default. It is dropped, not
-- replaced, so whatever excalibase_app left under its name goes with it.
DROP ROUTINE IF EXISTS excalibase.set_realtime_table(text, text, boolean);
CREATE FUNCTION excalibase.set_realtime_table(target_schema text, target_table text, publish boolean)
RETURNS void
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, pg_temp
AS $fn$
DECLARE
  target_publication CONSTANT text := %s;
  target_relation oid;
  current_identity "char";
  is_member boolean;
BEGIN
  SELECT c.oid, c.relreplident INTO target_relation, current_identity
  FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
  WHERE n.nspname = target_schema AND c.relname = target_table AND c.relkind = 'r'
    AND %s;
  IF target_relation IS NULL THEN
    RAISE EXCEPTION 'table %%.%% cannot be published', target_schema, target_table
      USING ERRCODE = 'insufficient_privilege';
  END IF;
  SELECT EXISTS (
    SELECT 1 FROM pg_publication_rel r JOIN pg_publication p ON p.oid = r.prpubid
    WHERE p.pubname = target_publication AND r.prrelid = target_relation
  ) INTO is_member;
  IF publish AND NOT is_member THEN
    EXECUTE format('ALTER PUBLICATION %%I ADD TABLE %%I.%%I', target_publication, target_schema, target_table);
  ELSIF NOT publish AND is_member THEN
    EXECUTE format('ALTER PUBLICATION %%I DROP TABLE %%I.%%I', target_publication, target_schema, target_table);
  END IF;
  IF publish AND current_identity <> 'f' THEN
    EXECUTE format('ALTER TABLE %%I.%%I REPLICA IDENTITY FULL', target_schema, target_table);
  ELSIF NOT publish AND current_identity <> 'd' THEN
    EXECUTE format('ALTER TABLE %%I.%%I REPLICA IDENTITY DEFAULT', target_schema, target_table);
  END IF;
END
$fn$;
REVOKE ALL ON FUNCTION excalibase.set_realtime_table(text, text, boolean) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION excalibase.set_realtime_table(text, text, boolean) TO %s;
`,
		authRole, safeAuthPass, // auth_admin DO block
		dbIdent, authRole, // GRANT CREATE ON DATABASE
		authRole, authRole, // GRANT auth schema
		appRole, safeAppPass, // excalibase_app DO block
		dbIdent, appRole, // GRANT CREATE ON DATABASE
		appRole, appRole, appRole, appRole, appRole, // GRANT public
		authSchemaRevokeSQL(appRole, authRole), // nothing in auth (EXC-628)
		reservedSchemaLockdownSQL(roleApp),     // reserved excalibase schema: USAGE only
		watcherRole, safeWatcherPass,           // cdc_watcher DO block
		watcherRole, watcherRole, dbIdent, // cdc_watcher registry schema + search_path
		pubName, pubName, // pubname check + CREATE PUBLICATION
		pubIdent,                             // ALTER PUBLICATION ... OWNER TO
		pubName, realtimeUserSchemaPredicate, // set_realtime_table body
		appRole, // GRANT EXECUTE
	) + reservedSchemaGuardSQL
}
