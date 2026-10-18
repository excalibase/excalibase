//go:build integration

package service

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/domain"
)

// EXC-628: a project's end-user auth tables (password hashes, token and key
// hashes) are reached with auth_admin alone. The role behind the API, Studio's
// SQL runner and MCP, and the owner login a function is handed, are refused.

// authServiceTables are the tables excalibase-auth's migrations create.
var authServiceTables = []string{
	"users", "refresh_tokens", "api_keys", "email_verification_tokens", "password_reset_tokens", "role_changes",
}

// seedAuthServiceTables is the auth service's own migration, run as auth_admin
// after the project was registered.
func (c *platformSchemaCase) seedAuthServiceTables(t *testing.T) {
	t.Helper()
	for _, table := range authServiceTables {
		mustExec(t, c.authAdmin, fmt.Sprintf(`CREATE TABLE IF NOT EXISTS auth.%s (id bigserial PRIMARY KEY, secret_hash text)`, table))
		mustExec(t, c.authAdmin, fmt.Sprintf(`INSERT INTO auth.%s (secret_hash) VALUES ('hash')`, table))
	}
}

func assertAuthTablesRefused(t *testing.T, db *sql.DB) {
	t.Helper()
	for _, table := range authServiceTables {
		mustFail(t, db, `SELECT * FROM auth.`+table, "permission denied")
	}
	mustFail(t, db, `SELECT nextval('auth.users_id_seq')`, "permission denied")
	mustFail(t, db, `INSERT INTO auth.users (secret_hash) VALUES ('planted')`, "permission denied")
}

func TestAuthTables_OnlyAuthAdminReachesThem(t *testing.T) {
	c := newPlatformSchemaCase(t)
	c.register(t)
	c.seedAuthServiceTables(t)

	assertAuthTablesRefused(t, c.app)
	assertAuthTablesRefused(t, c.owner(t))
	if scanBool(t, c.admin, `SELECT has_schema_privilege('excalibase_app', 'auth', 'USAGE')`) {
		t.Fatal("excalibase_app holds USAGE on the auth schema")
	}
	if got := scanString(t, c.authAdmin, `SELECT secret_hash FROM auth.users`); got != "hash" {
		t.Fatalf("auth_admin reads auth.users as %q", got)
	}
	mustExec(t, c.authAdmin, `UPDATE auth.refresh_tokens SET secret_hash = 'rotated'`)
}

// A project registered before EXC-628, or restored from a backup taken before
// it, carries the old read grants. Registering again, which a restore and the
// startup reconcile both do, takes them away, and the reconcile's probe sees
// the project as pending until it has.
func TestAuthTables_ReregistrationRevokesAnOlderProjectsGrants(t *testing.T) {
	c := newPlatformSchemaCase(t)
	c.register(t)
	c.seedAuthServiceTables(t)
	mustExec(t, c.admin, `GRANT USAGE ON SCHEMA auth TO excalibase_app;
		GRANT SELECT ON ALL TABLES IN SCHEMA auth TO excalibase_app;
		ALTER DEFAULT PRIVILEGES IN SCHEMA auth GRANT SELECT ON TABLES TO excalibase_app;
		ALTER DEFAULT PRIVILEGES FOR ROLE auth_admin IN SCHEMA auth GRANT SELECT ON TABLES TO excalibase_app;
		GRANT USAGE ON SCHEMA auth TO PUBLIC;
		GRANT SELECT ON auth.api_keys TO PUBLIC;
		GRANT auth_admin TO excalibase_app`)
	if got := scanString(t, c.app, `SELECT secret_hash FROM auth.users`); got != "hash" {
		t.Fatalf("precondition: the old grants let excalibase_app read auth.users, got %q", got)
	}
	ctx := context.Background()
	if pending, err := appReachesAuth(ctx, c.pg.execer(), "app"); err != nil || !pending {
		t.Fatalf("probe on the old grants = %v, %v; want pending", pending, err)
	}

	c.spec.resetPasswords = true
	c.register(t)

	assertAuthTablesRefused(t, c.app)
	assertAuthTablesRefused(t, c.owner(t))
	mustFail(t, c.app, `SET ROLE auth_admin`, "permission denied")
	mustExec(t, c.authAdmin, `CREATE TABLE auth.sessions (id int)`)
	mustFail(t, c.app, `SELECT * FROM auth.sessions`, "permission denied")
	if scanBool(t, c.admin, `SELECT EXISTS (SELECT 1 FROM pg_default_acl d
		JOIN pg_namespace n ON n.oid = d.defaclnamespace
		WHERE n.nspname = 'auth' AND d.defaclacl::text LIKE '%excalibase_app=%')`) {
		t.Fatal("a default privilege still hands excalibase_app the auth service's next table")
	}
	if pending, err := appReachesAuth(ctx, c.pg.execer(), "app"); err != nil || pending {
		t.Fatalf("probe after re-registration = %v, %v; want not pending", pending, err)
	}
}

// The snapshot export's own pg_dump, run against a real database, writes no
// auth table, not even one the request names.
func TestAuthTables_SnapshotExportLeavesThemOut(t *testing.T) {
	c := newPlatformSchemaCase(t)
	c.register(t)
	c.seedAuthServiceTables(t)
	mustExec(t, c.app, `CREATE TABLE public.notes (id int); INSERT INTO public.notes VALUES (7)`)

	for _, req := range []domain.SnapshotExportRequest{{}, {Tables: []string{"auth.users", "public.notes"}}} {
		dump, err := c.pg.execOutput(context.Background(), pgDumpCommand("app", "plain", req))
		if err != nil {
			t.Fatalf("pg_dump %+v: %v", req, err)
		}
		if !strings.Contains(dump, "public.notes") {
			t.Fatalf("the dump for %+v lost the project's own table", req)
		}
		if strings.Contains(dump, "secret_hash") || strings.Contains(dump, "auth.users") {
			t.Fatalf("the dump for %+v carries the auth schema", req)
		}
	}
}
