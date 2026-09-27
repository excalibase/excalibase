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

// BuildProjectRoleSQL produces the role-creation SQL run as the postgres
// superuser at provisioning time. Output:
//
//   - auth_admin   — owns the auth schema (used by excalibase-auth)
//   - excalibase_app — DML on public, read-only on auth. NO REPLICATION.
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

	return fmt.Sprintf(`
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
GRANT ALL ON SCHEMA public TO %s;
GRANT ALL ON ALL TABLES IN SCHEMA public TO %s;
GRANT ALL ON ALL SEQUENCES IN SCHEMA public TO %s;
ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT ALL ON TABLES TO %s;
ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT ALL ON SEQUENCES TO %s;
GRANT USAGE ON SCHEMA auth TO %s;
GRANT SELECT ON ALL TABLES IN SCHEMA auth TO %s;
ALTER DEFAULT PRIVILEGES IN SCHEMA auth GRANT SELECT ON TABLES TO %s;

-- Reserved schema for platform bookkeeping (scheduler and cron tables). It is
-- deliberately outside public, which the generated APIs expose. excalibase_app
-- needs CREATE here because it both applies the table DDL and moves the legacy
-- public tables in with ALTER TABLE ... SET SCHEMA, which requires CREATE on
-- the destination. Owning what it creates also means no default-privilege
-- grant is required for it to read its own tables afterwards.
CREATE SCHEMA IF NOT EXISTS excalibase;
GRANT USAGE, CREATE ON SCHEMA excalibase TO %s;
GRANT ALL ON ALL TABLES IN SCHEMA excalibase TO %s;
GRANT ALL ON ALL SEQUENCES IN SCHEMA excalibase TO %s;

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
CREATE OR REPLACE FUNCTION excalibase.set_realtime_table(target_schema text, target_table text, publish boolean)
RETURNS void
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, pg_temp
AS $fn$
DECLARE
  target_publication CONSTANT text := %s;
  target_relation oid;
  is_member boolean;
BEGIN
  SELECT c.oid INTO target_relation
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
END
$fn$;
REVOKE ALL ON FUNCTION excalibase.set_realtime_table(text, text, boolean) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION excalibase.set_realtime_table(text, text, boolean) TO %s;
`,
		authRole, safeAuthPass, // auth_admin DO block
		dbIdent, authRole, // GRANT CREATE ON DATABASE
		authRole, authRole, // GRANT auth schema
		appRole, safeAppPass, // excalibase_app DO block
		appRole, appRole, appRole, appRole, appRole, // GRANT public
		appRole, appRole, appRole, // GRANT auth read
		appRole, appRole, appRole, // GRANT reserved excalibase schema
		watcherRole, safeWatcherPass, // cdc_watcher DO block
		watcherRole, watcherRole, dbIdent, // cdc_watcher registry schema + search_path
		pubName, pubName, // pubname check + CREATE PUBLICATION
		pubIdent,                             // ALTER PUBLICATION ... OWNER TO
		pubName, realtimeUserSchemaPredicate, // set_realtime_table body
		appRole, // GRANT EXECUTE
	)
}
