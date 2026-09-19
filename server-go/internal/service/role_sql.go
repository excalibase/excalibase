package service

import (
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
		fmt.Fprintf(&b, "\nALTER ROLE %s WITH LOGIN PASSWORD %s;",
			schema.QuoteIdent(r.Role), schema.QuoteLiteral(r.Password))
	}
	return b.String()
}

// BuildProjectRoleSQL produces the role-creation SQL run as the postgres
// superuser at provisioning time. Output:
//
//   - auth_admin   — owns the auth schema (used by excalibase-auth)
//   - excalibase_app — DML on public, read-only on auth, owns the
//     publication for managing realtime membership. NO REPLICATION.
//   - cdc_watcher  — REPLICATION attribute, used by the watcher daemon
//     to consume the logical slot. No DML grants.
//   - empty publication, owned by excalibase_app so studio / graphql /
//     NoSQL auto-create can ALTER ADD/DROP TABLE. Name is configurable
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
	safeAuthPass := schema.QuoteLiteral(authPass)
	safeAppPass := schema.QuoteLiteral(appPass)
	safeWatcherPass := schema.QuoteLiteral(watcherPass)
	dbIdent := schema.QuoteIdent(dbName)

	return fmt.Sprintf(`
CREATE SCHEMA IF NOT EXISTS auth;

DO $$ BEGIN
  IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'auth_admin') THEN
    EXECUTE format('CREATE ROLE %s WITH LOGIN PASSWORD %%L', %s::text);
  END IF;
END $$;
GRANT CREATE ON DATABASE %s TO %s;
GRANT ALL ON SCHEMA auth TO %s;
ALTER DEFAULT PRIVILEGES IN SCHEMA auth GRANT ALL ON TABLES TO %s;

DO $$ BEGIN
  IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'excalibase_app') THEN
    EXECUTE format('CREATE ROLE %s WITH LOGIN PASSWORD %%L', %s::text);
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
    EXECUTE format('CREATE ROLE %s WITH LOGIN REPLICATION PASSWORD %%L', %s::text);
  END IF;
END $$;

-- Empty publication. Tables are added on demand:
--   - studio toggle UI (provisioning's /api/projects/{id}/realtime endpoints)
--   - NoSQL auto-create in graphql (when project's realtime_auto_enable=true)
-- Owner is excalibase_app so those code paths can ALTER ADD/DROP TABLE
-- without superuser escalation. The watcher daemon's config is the source
-- of truth for the publication name; both ends must agree.
DO $$ BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_publication WHERE pubname = %s) THEN
    CREATE PUBLICATION %s;
  END IF;
END $$;
ALTER PUBLICATION %s OWNER TO %s;
`,
		authRole, safeAuthPass, // auth_admin DO block
		dbIdent, authRole, // GRANT CREATE ON DATABASE
		authRole, authRole, // GRANT auth schema
		appRole, safeAppPass, // excalibase_app DO block
		appRole, appRole, appRole, appRole, appRole, // GRANT public
		appRole, appRole, appRole, // GRANT auth read
		appRole, appRole, appRole, // GRANT reserved excalibase schema
		watcherRole, safeWatcherPass, // cdc_watcher DO block
		schema.QuoteLiteral(publicationName), // pubname check (literal)
		pubIdent,                             // CREATE PUBLICATION ident
		pubIdent, appRole,                    // ALTER PUBLICATION ... OWNER TO
	)
}
