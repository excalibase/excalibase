//go:build integration

package service

import (
	"database/sql"
	"testing"
)

// Migrations and Studio run as excalibase_app, so it owns what they create.
// The project's own login (the one DATABASE_URL carries into its containers)
// must be able to use those tables, and still reach nothing of the platform.

func TestOwnerAccessOnPostgres15(t *testing.T) { ownerAccessSuite(t, "postgres:15-alpine") }

func TestOwnerAccessOnPostgres17(t *testing.T) { ownerAccessSuite(t, "postgres:17-alpine") }

type ownerAccessCase struct {
	pg    *rolePostgres
	owner *sql.DB
	app   *sql.DB
	admin *sql.DB
	spec  projectRoleSpec
	creds projectRoleCredentials
}

func ownerAccessSuite(t *testing.T, image string) {
	pg := startRolePostgresImage(t, image)
	pg.psql(t, customerClusterSetupSQL())
	c := &ownerAccessCase{
		pg:    pg,
		spec:  projectRoleSpec{databaseName: "app", adminUsername: customerOwner},
		creds: projectRoleCredentials{authPassword: "Auth-Password-1", appPassword: customerAppPassword, watcherPassword: "Watcher-Password-1"},
	}
	pg.psql(t, projectRoleSQL(c.spec, c.creds, "cdc_watcher_pub"))
	c.owner = pg.connect(t, customerOwner, customerOwnerPassword)
	c.app = pg.connect(t, roleApp, customerAppPassword)
	c.admin = pg.connect(t, "postgres", "p")

	t.Run("a migration creates a schema", c.migrationCreatesSchema)
	t.Run("owner uses a migrated table in public", c.ownerUsesPublicTable)
	t.Run("owner uses a migrated table in a migrated schema", c.ownerUsesSchemaTable)
	t.Run("owner uses a table created inside CREATE SCHEMA", c.ownerUsesSchemaElement)
	t.Run("platform keeps using the tables it created", c.platformKeepsAccess)
	t.Run("owner reaches no platform bookkeeping", c.ownerReachesNoBookkeeping)
	t.Run("neither role becomes the other", c.noMembershipEitherWay)
	t.Run("owner DDL is unaffected", c.ownerDDLUnaffected)
	t.Run("owner gets no trigger on a migrated table", c.ownerGetsNoTrigger)
	t.Run("owner uses a migrated view", c.ownerUsesView)
	t.Run("re-registration keeps access", c.reRegistrationKeepsAccess)
	t.Run("a routine planted under a platform name cannot stop registration", c.plantedRoutinesAreReplaced)
}

// A trigger runs as whoever writes the row: one the owner could attach to a
// migrated table would run its code as excalibase_app on every API write.
func (c *ownerAccessCase) ownerGetsNoTrigger(t *testing.T) {
	for _, privilege := range []string{"TRIGGER", "REFERENCES"} {
		if scanBool(t, c.admin, "SELECT has_table_privilege($1, 'public.teams', $2)", customerOwner, privilege) {
			t.Errorf("the owner holds %s on a migrated table", privilege)
		}
	}
	mustExec(t, c.owner, "CREATE FUNCTION public.steal() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RETURN NEW; END $$")
	mustFail(t, c.owner, "CREATE TRIGGER steal BEFORE INSERT ON public.teams FOR EACH ROW EXECUTE FUNCTION public.steal()", "")
	mustExec(t, c.owner, "TRUNCATE public.teams")
}

func (c *ownerAccessCase) ownerUsesView(t *testing.T) {
	mustExec(t, c.app, "CREATE VIEW league.match_homes AS SELECT home FROM league.matches")
	if got := scanString(t, c.owner, "SELECT count(*)::text FROM league.match_homes"); got == "" {
		t.Fatal("the owner could not read a migrated view")
	}
}

// excalibase_app can create objects in the excalibase schema; a procedure or
// aggregate it left under a platform function's name must not abort the
// registration that replaces it.
func (c *ownerAccessCase) plantedRoutinesAreReplaced(t *testing.T) {
	c.pg.psql(t, "DROP EVENT TRIGGER excalibase_share_created_schema; DROP FUNCTION excalibase.share_created_schema(); DROP FUNCTION excalibase.share_with_owner(oid); DROP FUNCTION excalibase.drop_role(text)")
	mustExec(t, c.app, "CREATE PROCEDURE excalibase.share_with_owner(oid) LANGUAGE sql AS $$ SELECT 1 $$")
	mustExec(t, c.app, "CREATE PROCEDURE excalibase.share_created_schema() LANGUAGE sql AS $$ SELECT 1 $$")
	mustExec(t, c.app, "CREATE PROCEDURE excalibase.drop_role(text) LANGUAGE sql AS $$ SELECT 1 $$")
	restore := c.spec
	restore.resetPasswords = true
	if err := c.pg.psqlErr(projectRoleSQL(restore, c.creds, "cdc_watcher_pub")); err != nil {
		t.Fatalf("registration failed on a planted routine: %v", err)
	}
	mustExec(t, c.app, "CREATE SCHEMA after_plant")
	mustExec(t, c.app, "CREATE TABLE after_plant.items (id int)")
	mustExec(t, c.owner, "INSERT INTO after_plant.items VALUES (1)")
}

func (c *ownerAccessCase) migrationCreatesSchema(t *testing.T) {
	mustExec(t, c.app, "CREATE SCHEMA league")
}

func (c *ownerAccessCase) ownerUsesPublicTable(t *testing.T) {
	mustExec(t, c.app, "CREATE TABLE public.teams (id serial PRIMARY KEY, name text NOT NULL)")
	mustExec(t, c.owner, "INSERT INTO public.teams (name) VALUES ('Arsenal')")
	mustExec(t, c.owner, "UPDATE public.teams SET name = 'Arsenal FC' WHERE id = 1")
	if got := scanString(t, c.owner, "SELECT name FROM public.teams WHERE id = 1"); got != "Arsenal FC" {
		t.Fatalf("owner read %q", got)
	}
	mustExec(t, c.owner, "DELETE FROM public.teams WHERE id = 1")
}

func (c *ownerAccessCase) ownerUsesSchemaTable(t *testing.T) {
	mustExec(t, c.app, "CREATE TABLE league.matches (id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY, code serial, home text)")
	mustExec(t, c.owner, "INSERT INTO league.matches (home) VALUES ('Leeds')")
	if got := scanString(t, c.owner, "SELECT home FROM league.matches"); got != "Leeds" {
		t.Fatalf("owner read %q", got)
	}
}

func (c *ownerAccessCase) ownerUsesSchemaElement(t *testing.T) {
	mustExec(t, c.app, "CREATE SCHEMA stats CREATE TABLE scores (id serial PRIMARY KEY, points int)")
	mustExec(t, c.owner, "INSERT INTO stats.scores (points) VALUES (3)")
	if got := scanString(t, c.owner, "SELECT points::text FROM stats.scores"); got != "3" {
		t.Fatalf("owner read %q", got)
	}
}

func (c *ownerAccessCase) platformKeepsAccess(t *testing.T) {
	mustExec(t, c.app, "INSERT INTO league.matches (home) VALUES ('Wolves')")
	mustExec(t, c.app, "ALTER TABLE league.matches ADD COLUMN away text")
	mustExec(t, c.app, "DROP TABLE stats.scores")
}

// The reserved schemas hold scheduler and cron state the platform writes;
// a table the platform creates there stays the platform's.
func (c *ownerAccessCase) ownerReachesNoBookkeeping(t *testing.T) {
	mustExec(t, c.app, "CREATE TABLE excalibase.job_runs (id int)")
	mustFail(t, c.owner, "SELECT * FROM excalibase.job_runs", "permission denied")
	mustFail(t, c.owner, "CREATE TABLE excalibase.planted (id int)", "permission denied")
}

func (c *ownerAccessCase) noMembershipEitherWay(t *testing.T) {
	if scanBool(t, c.admin, "SELECT pg_has_role($1, $2, 'MEMBER')", customerOwner, roleApp) {
		t.Fatal("the owner became a member of the platform role")
	}
	if scanBool(t, c.admin, "SELECT pg_has_role($1, $2, 'MEMBER')", roleApp, customerOwner) {
		t.Fatal("the platform role became a member of the owner")
	}
	mustFail(t, c.owner, "SET ROLE "+roleApp, "permission denied")
}

// The owner's own DDL still works and stays its own; the trigger only acts
// for the platform role.
func (c *ownerAccessCase) ownerDDLUnaffected(t *testing.T) {
	mustExec(t, c.owner, "CREATE SCHEMA mine")
	mustExec(t, c.owner, "CREATE TABLE mine.notes (id int)")
	if scanBool(t, c.admin, "SELECT has_schema_privilege($1, 'mine', 'USAGE')", roleApp) {
		t.Fatal("the owner's schema was shared with the platform role by the trigger")
	}
}

func (c *ownerAccessCase) reRegistrationKeepsAccess(t *testing.T) {
	restore := c.spec
	restore.resetPasswords = true
	c.pg.psql(t, projectRoleSQL(restore, c.creds, "cdc_watcher_pub"))
	mustExec(t, c.app, "CREATE SCHEMA later")
	mustExec(t, c.app, "CREATE TABLE later.items (id serial PRIMARY KEY)")
	mustExec(t, c.owner, "INSERT INTO later.items DEFAULT VALUES")
	mustExec(t, c.owner, "INSERT INTO public.teams (name) VALUES ('Spurs')")
}
