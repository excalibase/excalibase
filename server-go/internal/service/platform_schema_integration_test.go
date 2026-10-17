//go:build integration

package service

import (
	"database/sql"
	"strings"
	"testing"
)

// EXC-623: customer-written SQL (Studio's runner, MCP execute_sql and
// apply_migration) runs as excalibase_app. It keeps read access to the
// project's auth tables and its own schemas, and gets no write, CREATE or
// ownership in the platform's excalibase schema. The scheduler's bookkeeping
// there is written by excalibase_scheduler, a login no customer path uses.

const (
	platformSchedulerPassword = "Scheduler-Password-1"
	platformAuthPassword      = "Auth-Password-1"
)

type platformSchemaCase struct {
	pg        *rolePostgres
	spec      projectRoleSpec
	creds     projectRoleCredentials
	admin     *sql.DB
	app       *sql.DB
	scheduler *sql.DB
	authAdmin *sql.DB
}

func newPlatformSchemaCase(t *testing.T) *platformSchemaCase {
	t.Helper()
	return newPlatformSchemaCaseOn(t, startRolePostgres(t))
}

func newPlatformSchemaCaseOn(t *testing.T, pg *rolePostgres) *platformSchemaCase {
	t.Helper()
	pg.psql(t, customerClusterSetupSQL())
	return &platformSchemaCase{
		pg:   pg,
		spec: projectRoleSpec{databaseName: "app", adminUsername: customerOwner},
		creds: projectRoleCredentials{
			authPassword: platformAuthPassword, appPassword: customerAppPassword,
			watcherPassword: "Watcher-Password-1", schedulerPassword: platformSchedulerPassword,
		},
		admin: pg.connect(t, "postgres", "p"),
	}
}

// register runs the registration SQL the way provisioning, a restore and the
// startup reconcile all do, then logs every platform role in.
func (c *platformSchemaCase) register(t *testing.T) {
	t.Helper()
	c.pg.psql(t, projectRoleSQL(c.spec, c.creds, "cdc_watcher_pub"))
	c.app = c.pg.connect(t, roleApp, customerAppPassword)
	c.scheduler = c.pg.connect(t, roleScheduler, platformSchedulerPassword)
	c.authAdmin = c.pg.connect(t, roleAuthAdmin, platformAuthPassword)
}

// seedAuthUsers is the auth service's own migration: auth_admin creates its
// tables after the project was registered.
func (c *platformSchemaCase) seedAuthUsers(t *testing.T) {
	t.Helper()
	mustExec(t, c.authAdmin, `CREATE TABLE IF NOT EXISTS auth.users (id serial PRIMARY KEY, email text NOT NULL)`)
	mustExec(t, c.authAdmin, `INSERT INTO auth.users (email) VALUES ('ada@example.test')`)
}

func TestPlatformSchema_CustomerSQLCannotWriteIt(t *testing.T) {
	c := newPlatformSchemaCase(t)
	c.register(t)
	c.seedAuthUsers(t)

	refused := []string{
		`CREATE TABLE excalibase.planted (id int)`,
		`CREATE FUNCTION excalibase.planted() RETURNS int LANGUAGE sql SECURITY DEFINER AS 'SELECT 1'`,
		`CREATE OR REPLACE FUNCTION excalibase.share_with_owner(target_schema oid) RETURNS void LANGUAGE sql AS ''`,
		`CREATE VIEW excalibase.planted_view AS SELECT 1 AS one`,
		`CREATE TYPE excalibase.planted_type AS (one int)`,
		`ALTER FUNCTION excalibase.create_role(text, text, boolean, text[]) OWNER TO excalibase_app`,
		`ALTER FUNCTION excalibase.set_realtime_table(text, text, boolean) SECURITY INVOKER`,
		`DROP FUNCTION excalibase.drop_role(text)`,
		`INSERT INTO excalibase.excalibase_scheduled_functions (id, project_id, module_name, export_name, args, scheduled_for)
		 VALUES ('t1', 'p', 'm', 'e', '{}', now())`,
		`UPDATE excalibase.excalibase_cron_jobs SET args = '{}'`,
		`DELETE FROM excalibase.excalibase_scheduled_functions`,
		`TRUNCATE excalibase.excalibase_cron_jobs`,
		`CREATE TRIGGER planted BEFORE INSERT ON excalibase.excalibase_cron_jobs FOR EACH ROW EXECUTE FUNCTION public.app_trigger()`,
		`ALTER TABLE excalibase.excalibase_cron_jobs ADD COLUMN planted int`,
		`INSERT INTO auth.users (email) VALUES ('mallory@example.test')`,
		`CREATE TABLE excalibase_cdc.planted (id int)`,
		// A trusted extension checks CREATE on the database, not the schema.
		`CREATE EXTENSION pgcrypto SCHEMA excalibase`,
		`CREATE EXTENSION hstore SCHEMA auth`,
	}
	mustExec(t, c.app, `CREATE FUNCTION public.app_trigger() RETURNS trigger LANGUAGE plpgsql SECURITY DEFINER AS $$ BEGIN RETURN NEW; END $$`)
	for _, statement := range refused {
		mustBeRefused(t, c.app, statement)
	}

	mustExec(t, c.app, `CREATE EXTENSION citext`)
	mustExec(t, c.app, `CREATE TABLE public.moved (id int)`)
	mustFail(t, c.app, `ALTER TABLE public.moved SET SCHEMA excalibase`, "permission denied")

	if got := scanString(t, c.app, `SELECT email FROM auth.users`); got != "ada@example.test" {
		t.Fatalf("customer SQL reads auth.users as %q, want the project's own user", got)
	}
	if scanBool(t, c.admin, `SELECT has_schema_privilege('excalibase_app', 'excalibase', 'CREATE')`) {
		t.Fatal("excalibase_app still holds CREATE on the excalibase schema")
	}
	if n := c.platformObjectsNotOwnedBySuperuser(t); n != 0 {
		t.Fatalf("%d object(s) in the excalibase schema belong to a non-superuser", n)
	}
}

func TestPlatformSchema_CustomerSQLKeepsItsOwnWork(t *testing.T) {
	c := newPlatformSchemaCase(t)
	c.register(t)

	mustExec(t, c.app, `CREATE TABLE public.notes (id serial PRIMARY KEY, body text)`)
	mustExec(t, c.app, `INSERT INTO public.notes (body) VALUES ('hello')`)
	mustExec(t, c.app, `CREATE SCHEMA billing`)
	mustExec(t, c.app, `CREATE FUNCTION public.touch() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RETURN NEW; END $$`)
	mustExec(t, c.app, `CREATE TRIGGER notes_touch BEFORE UPDATE ON public.notes FOR EACH ROW EXECUTE FUNCTION public.touch()`)
	// Studio and realtime still call the platform's guarded functions.
	mustExec(t, c.app, `SELECT excalibase.create_role('studio_reader', 'Reader-1', true, '{}')`)
	mustExec(t, c.app, `SELECT excalibase.set_realtime_table('public', 'notes', true)`)
	mustExec(t, c.app, `SELECT excalibase.set_realtime_table('public', 'notes', false)`)
	mustFailArgs(t, c.app, `SELECT excalibase.set_realtime_table('excalibase', 'excalibase_cron_jobs', true)`, nil, "cannot be published")
}

func TestPlatformSchema_PlatformRolesStillWriteTheirTables(t *testing.T) {
	c := newPlatformSchemaCase(t)
	c.register(t)
	c.seedAuthUsers(t)

	mustExec(t, c.authAdmin, `UPDATE auth.users SET email = 'ada@example.org'`)
	mustExec(t, c.scheduler, `INSERT INTO excalibase.excalibase_scheduled_functions
		(id, project_id, module_name, export_name, args, scheduled_for) VALUES ('t1', 'p', 'jobs', 'run', '{}', now())`)
	mustExec(t, c.scheduler, `INSERT INTO excalibase.excalibase_cron_jobs
		(name, project_id, function_id, module_name, export_name, args, schedule) VALUES ('nightly', 'p', 'f', 'jobs', 'run', '{}', '{}')`)
	tx, err := c.scheduler.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`SELECT id FROM excalibase.excalibase_scheduled_functions WHERE status = 'pending' FOR UPDATE SKIP LOCKED`); err != nil {
		t.Fatalf("scheduler cannot claim a due task: %v", err)
	}
	if _, err := tx.Exec(`UPDATE excalibase.excalibase_scheduled_functions SET status = 'running', claimed_at = now() WHERE id = 't1'`); err != nil {
		t.Fatalf("scheduler cannot mark a task: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	mustExec(t, c.scheduler, `DELETE FROM excalibase.excalibase_cron_jobs WHERE function_id = 'f'`)

	// The scheduler role writes its two tables and nothing else.
	for _, statement := range []string{
		`CREATE TABLE excalibase.planted (id int)`,
		`CREATE TABLE public.planted (id int)`,
		`SELECT * FROM auth.users`,
		`SELECT excalibase.create_role('sched_made', 'X-1', true, '{}')`,
	} {
		mustBeRefused(t, c.scheduler, statement)
	}
	expectPlainRole(t, c.admin, roleScheduler)
	if path := scanString(t, c.admin, `SELECT array_to_string(setconfig, ',') FROM pg_db_role_setting s
		JOIN pg_roles r ON r.oid = s.setrole WHERE r.rolname = $1`, roleScheduler); path != "search_path=pg_catalog, pg_temp" {
		t.Fatalf("scheduler search_path setting = %q", path)
	}
}

// A project registered before EXC-623 handed excalibase_app CREATE on the
// schema and let it own the scheduler tables. Registering again, which the
// startup reconcile does, leaves nothing it planted there and nothing it owns.
func TestPlatformSchema_ReregistrationClearsWhatTheAppRolePlanted(t *testing.T) {
	c := newPlatformSchemaCase(t)
	c.register(t)
	mustExec(t, c.admin, `DROP TABLE excalibase.excalibase_scheduled_functions, excalibase.excalibase_cron_jobs`)
	mustExec(t, c.admin, `GRANT CREATE ON SCHEMA excalibase TO excalibase_app`)
	mustExec(t, c.app, `CREATE TABLE excalibase.excalibase_cron_jobs (name text, project_id text, args jsonb)`)
	mustExec(t, c.app, `CREATE FUNCTION excalibase.planted() RETURNS trigger LANGUAGE plpgsql SECURITY DEFINER
		AS $$ BEGIN RETURN NEW; END $$`)
	mustExec(t, c.app, `CREATE TRIGGER planted BEFORE INSERT ON excalibase.excalibase_cron_jobs
		FOR EACH ROW EXECUTE FUNCTION excalibase.planted()`)
	mustExec(t, c.app, `CREATE FUNCTION excalibase.create_role(role_name text) RETURNS void LANGUAGE sql AS ''`)

	// A trigger needs no ownership, only the TRIGGER grant the app role held.
	mustExec(t, c.admin, `CREATE TABLE excalibase.platform_jobs (id int)`)
	mustExec(t, c.admin, `GRANT TRIGGER ON excalibase.platform_jobs TO excalibase_app`)
	mustExec(t, c.app, `CREATE FUNCTION public.app_trigger() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RETURN NEW; END $$`)
	mustExec(t, c.app, `CREATE TRIGGER planted_on_platform BEFORE INSERT ON excalibase.platform_jobs FOR EACH ROW EXECUTE FUNCTION public.app_trigger()`)

	c.register(t)

	if n := c.platformObjectsNotOwnedBySuperuser(t); n != 0 {
		t.Fatalf("%d object(s) planted by excalibase_app survived re-registration", n)
	}
	if scanBool(t, c.admin, `SELECT has_schema_privilege('excalibase_app', 'excalibase', 'CREATE')`) {
		t.Fatal("re-registration left excalibase_app CREATE on the excalibase schema")
	}
	if scanBool(t, c.admin, `SELECT EXISTS (SELECT 1 FROM pg_trigger WHERE tgname IN ('planted', 'planted_on_platform'))`) {
		t.Fatal("a planted trigger survived re-registration")
	}
	// Everything registration owns is back, and the customer's paths still work.
	mustExec(t, c.scheduler, `INSERT INTO excalibase.excalibase_cron_jobs
		(name, project_id, function_id, module_name, export_name, args, schedule) VALUES ('n', 'p', 'f', 'm', 'e', '{}', '{}')`)
	mustExec(t, c.owner(t), `SELECT excalibase.create_role('after_rebuild', 'After-1', true, '{}')`)
	if !scanBool(t, c.admin, `SELECT EXISTS (SELECT 1 FROM pg_event_trigger WHERE evtname = 'excalibase_share_created_schema')`) {
		t.Fatal("the owner-access event trigger was not put back")
	}

	// A clean schema is left as it is: the scheduler's rows survive.
	c.register(t)
	if !scanBool(t, c.admin, `SELECT EXISTS (SELECT 1 FROM excalibase.excalibase_cron_jobs WHERE name = 'n')`) {
		t.Fatal("re-registering a clean project dropped the scheduler's rows")
	}
}

// Customer SQL may still record default privileges for itself in the schema;
// they are not objects there, so they must not make the next registration
// drop the scheduler's rows, and registration clears them.
func TestPlatformSchema_AppRoleDefaultPrivilegesDoNotCostTheSchedulerItsRows(t *testing.T) {
	c := newPlatformSchemaCase(t)
	c.register(t)
	mustExec(t, c.scheduler, `INSERT INTO excalibase.excalibase_scheduled_functions
		(id, project_id, module_name, export_name, args, scheduled_for) VALUES ('kept', 'p', 'm', 'e', '{}', now())`)
	for _, statement := range []string{
		`ALTER DEFAULT PRIVILEGES IN SCHEMA excalibase GRANT SELECT ON TABLES TO PUBLIC`,
		`ALTER DEFAULT PRIVILEGES IN SCHEMA excalibase GRANT USAGE ON SEQUENCES TO PUBLIC`,
		`ALTER DEFAULT PRIVILEGES IN SCHEMA excalibase GRANT EXECUTE ON FUNCTIONS TO ` + customerOwner,
	} {
		mustExec(t, c.app, statement)
	}

	c.register(t)

	if !scanBool(t, c.admin, `SELECT EXISTS (SELECT 1 FROM excalibase.excalibase_scheduled_functions WHERE id = 'kept')`) {
		t.Fatal("a default privilege the app role set made registration drop the scheduler's rows")
	}
	if scanBool(t, c.admin, `SELECT EXISTS (SELECT 1 FROM pg_default_acl d JOIN pg_namespace n ON n.oid = d.defaclnamespace
		WHERE n.nspname = 'excalibase')`) {
		t.Fatal("registration left the app role's default privileges in the excalibase schema")
	}
}

func (c *platformSchemaCase) owner(t *testing.T) *sql.DB {
	t.Helper()
	return c.pg.connect(t, customerOwner, customerOwnerPassword)
}

// platformObjectsNotOwnedBySuperuser counts every object of any kind in the
// excalibase schema whose owner is not a superuser, the schema included.
func (c *platformSchemaCase) platformObjectsNotOwnedBySuperuser(t *testing.T) int {
	t.Helper()
	var count int
	if err := c.admin.QueryRow(`
SELECT count(*) FROM pg_depend d
JOIN pg_shdepend s ON s.classid = d.classid AND s.objid = d.objid AND s.deptype = 'o'
  AND s.dbid = (SELECT oid FROM pg_database WHERE datname = current_database())
JOIN pg_roles r ON r.oid = s.refobjid
WHERE d.refclassid = 'pg_catalog.pg_namespace'::regclass
  AND d.refobjid = 'excalibase'::regnamespace AND NOT r.rolsuper`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if scanBool(t, c.admin, `SELECT NOT r.rolsuper FROM pg_namespace n JOIN pg_roles r ON r.oid = n.nspowner WHERE n.nspname = 'excalibase'`) {
		count++
	}
	return count
}

// mustBeRefused expects Postgres to refuse statement for want of a privilege
// or of ownership, not for any other reason.
func mustBeRefused(t *testing.T, db *sql.DB, statement string) {
	t.Helper()
	_, err := db.Exec(statement)
	if err == nil {
		t.Fatalf("allowed: %s", statement)
	}
	if !strings.Contains(err.Error(), "permission denied") && !strings.Contains(err.Error(), "must be owner") {
		t.Fatalf("%s refused for another reason: %v", statement, err)
	}
}
