//go:build integration

package service

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// EXC-821 review round 2: who counts as the platform, and a repair that keeps
// what it did when a later step refuses.

const grantCreateOnAuth = `GRANT CREATE, USAGE ON SCHEMA auth TO `

// plantRoutine puts a routine owned by owner into schema.
func plantRoutine(t *testing.T, c *platformSchemaCase, schema, name, owner string) {
	t.Helper()
	mustExec(t, c.admin, `CREATE FUNCTION `+schema+`.`+name+`() RETURNS int LANGUAGE sql AS 'SELECT 1'`)
	mustExec(t, c.admin, `ALTER FUNCTION `+schema+`.`+name+`() OWNER TO `+owner)
}

func (c *platformSchemaCase) inSchema(t *testing.T, schema, name string) bool {
	t.Helper()
	return scanBool(t, c.admin, `SELECT EXISTS (SELECT 1 FROM pg_proc WHERE proname = $1 AND pronamespace = $2::regnamespace)`, name, schema)
}

func (c *platformSchemaCase) inAnyQuarantine(t *testing.T, name string) bool {
	t.Helper()
	return scanBool(t, c.admin, `SELECT EXISTS (SELECT 1 FROM pg_proc p JOIN pg_namespace n ON n.oid = p.pronamespace
		WHERE p.proname = $1 AND n.nspname LIKE 'excalibase\_quarantine\_%')`, name)
}

// Finding 1: a role is the platform's by exact name, never by prefix. The owner
// can create excalibase_helper; it is a customer and loses its rights.
func TestPlatformSchemas_APlatformLookAlikeRoleIsACustomer(t *testing.T) {
	c := newPlatformSchemaCase(t)
	c.register(t)
	c.seedAuthUsers(t)
	mustExec(t, c.owner(t), `CREATE ROLE excalibase_helper NOLOGIN`)
	mustExec(t, c.admin, grantCreateOnAuth+`excalibase_helper`)
	plantRoutine(t, c, "auth", "g", "excalibase_helper")
	if !c.probe(t) {
		t.Fatal("the probe trusted a role only for its name's prefix")
	}
	c.register(t)
	if scanBool(t, c.admin, `SELECT has_schema_privilege('excalibase_helper', 'auth', 'CREATE')`) {
		t.Fatal("the look-alike role still holds CREATE on auth")
	}
	if c.inSchema(t, "auth", "g") || !c.inAnyQuarantine(t, "g") {
		t.Fatal("the look-alike role's routine was trusted in auth")
	}
	if c.probe(t) {
		t.Fatal("drift left behind")
	}
}

// Finding 1: a role bearing an exact platform name that the owner administers is
// the owner's. Registration refuses on it, and the revokes still land first.
func TestPlatformSchemas_APlatformNamedRoleTheOwnerAdministersIsACustomer(t *testing.T) {
	c := newPlatformSchemaCase(t)
	c.register(t)
	mustExec(t, c.admin, `GRANT documentdb TO `+customerOwner+` WITH ADMIN OPTION`)
	mustExec(t, c.admin, grantCreateOnAuth+`documentdb`)
	if !c.probe(t) {
		t.Fatal("the probe trusted an owner-administered role for its exact name")
	}
	if err := c.registerWith(t, ""); err == nil {
		t.Fatal("registration went on past a platform role the owner administers")
	}
	if scanBool(t, c.admin, `SELECT has_schema_privilege('documentdb', 'auth', 'CREATE')`) {
		t.Fatal("the revokes did not commit before the refusal")
	}
}

// Finding 2: customers hold the runtime and reader passwords, so they are
// customers for schema rights, and keep what registration gives them elsewhere.
func TestPlatformSchemas_RuntimeAndReaderAreCustomersForSchemaRights(t *testing.T) {
	c := newPlatformSchemaCase(t)
	c.creds.runtimePassword, c.creds.readerPassword = "Runtime-Password-1", "Reader-Password-1"
	c.register(t)
	mustExec(t, c.admin, `CREATE TABLE public.shared_t (id int)`)
	mustExec(t, c.admin, `GRANT CREATE, USAGE ON SCHEMA auth TO excalibase_runtime, excalibase_reader`)
	mustExec(t, c.admin, `GRANT CREATE, USAGE ON SCHEMA excalibase_cdc TO excalibase_runtime`)
	plantRoutine(t, c, "auth", "rt", "excalibase_runtime")
	plantRoutine(t, c, "excalibase_cdc", "rd", "excalibase_reader")
	if !c.probe(t) {
		t.Fatal("the probe trusted the runtime and reader logins in the platform schemas")
	}
	c.register(t)
	for _, role := range []string{"excalibase_runtime", "excalibase_reader"} {
		if scanBool(t, c.admin, `SELECT has_schema_privilege($1, 'auth', 'CREATE') OR has_schema_privilege($1, 'excalibase_cdc', 'CREATE')`, role) {
			t.Errorf("%s still holds CREATE in a platform schema", role)
		}
	}
	if c.inSchema(t, "auth", "rt") || c.inSchema(t, "excalibase_cdc", "rd") {
		t.Fatal("a routine of the runtime or reader login stayed in a platform schema")
	}
	if !scanBool(t, c.admin, `SELECT has_table_privilege('excalibase_reader', 'public.shared_t', 'SELECT')`) {
		t.Fatal("the reader lost the read grant registration gives it")
	}
}

// Finding 3: a quarantine schema the customer made first cannot stop the
// repair. The revokes and the trigger drop commit; the schema is taken over.
func TestPlatformSchemas_ASquattedQuarantineSchemaIsTakenOver(t *testing.T) {
	c := newPlatformSchemaCase(t)
	c.register(t)
	c.seedAuthUsers(t)
	mustExec(t, c.admin, `CREATE SCHEMA excalibase_quarantine_auth AUTHORIZATION excalibase_app`)
	mustExec(t, c.admin, `GRANT CREATE ON SCHEMA excalibase_quarantine_auth TO PUBLIC`)
	plantRoutine(t, c, "excalibase_quarantine_auth", "f", "excalibase_app")
	plantRoutine(t, c, "auth", "f", "excalibase_app")
	mustExec(t, c.admin, grantCreateOnAuth+`excalibase_app`)
	mustExec(t, c.admin, `CREATE FUNCTION public.tg() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RETURN NEW; END $$`)
	mustExec(t, c.admin, `CREATE TRIGGER planted BEFORE INSERT ON auth.users FOR EACH ROW EXECUTE FUNCTION public.tg()`)

	c.register(t)

	if scanBool(t, c.admin, `SELECT has_schema_privilege('excalibase_app', 'auth', 'CREATE')`) {
		t.Fatal("the customer kept CREATE on auth")
	}
	if scanBool(t, c.admin, `SELECT EXISTS (SELECT 1 FROM pg_trigger WHERE tgname = 'planted')`) {
		t.Fatal("the planted trigger survived")
	}
	if c.inSchema(t, "auth", "f") {
		t.Fatal("the colliding routine stayed in auth")
	}
	if !scanBool(t, c.admin, `SELECT NOT EXISTS (SELECT 1 FROM pg_namespace n WHERE n.nspname LIKE 'excalibase\_quarantine\_%'
		AND (n.nspowner = 'excalibase_app'::regrole OR has_schema_privilege('public', n.oid, 'CREATE') OR has_schema_privilege('excalibase_app', n.oid, 'CREATE')))`) {
		t.Fatal("a quarantine schema still belongs to the customer or lets it create")
	}
	if c.probe(t) {
		t.Fatal("drift left behind")
	}
}

// Finding 3: a table the repair leaves for an operator no longer costs the
// revokes, the trigger drops or a password repair; registration reports it.
func TestPlatformSchemas_ARefusalKeepsTheRevokesAndThePasswordRepair(t *testing.T) {
	c := newPlatformSchemaCase(t)
	c.register(t)
	c.seedAuthUsers(t)
	mustExec(t, c.admin, grantCreateOnAuth+`excalibase_app`)
	mustExec(t, c.admin, `CREATE TABLE auth.note (id int)`)
	mustExec(t, c.admin, `ALTER TABLE auth.note OWNER TO excalibase_app`)
	mustExec(t, c.admin, `CREATE FUNCTION public.tg() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RETURN NEW; END $$`)
	mustExec(t, c.admin, `CREATE TRIGGER planted BEFORE INSERT ON auth.users FOR EACH ROW EXECUTE FUNCTION public.tg()`)
	mustExec(t, c.app, `ALTER ROLE excalibase_app PASSWORD 'changed-in-sql'`)
	repair, err := passwordRepairSQL([]RolePassword{{Role: roleApp, Password: customerAppPassword}})
	if err != nil {
		t.Fatal(err)
	}

	err = c.registerWith(t, repair)

	if err == nil || !strings.Contains(err.Error(), "left for an operator") {
		t.Fatalf("registration did not report the table it left: %v", err)
	}
	if scanBool(t, c.admin, `SELECT has_schema_privilege('excalibase_app', 'auth', 'CREATE')`) {
		t.Fatal("the revokes were rolled back by the refusal")
	}
	if scanBool(t, c.admin, `SELECT EXISTS (SELECT 1 FROM pg_trigger WHERE tgname = 'planted')`) {
		t.Fatal("the trigger drop was rolled back by the refusal")
	}
	if err := c.app.Ping(); err != nil {
		t.Fatalf("the password repair was rolled back by the refusal: %v", err)
	}
	if !scanBool(t, c.admin, `SELECT to_regclass('auth.note') IS NOT NULL AND 'auth.note'::regclass::oid IN (SELECT oid FROM pg_class WHERE relowner = 'excalibase_app'::regrole)`) {
		t.Fatal("the unknown customer table was moved or adopted")
	}
}

// Finding 4: a platform table whose owner drifted is taken back in place, never
// sent into quarantine, and the customer cannot read it.
func TestPlatformSchemas_ADriftedPlatformTableIsReclaimedNotQuarantined(t *testing.T) {
	c := newPlatformSchemaCase(t)
	c.register(t)
	c.seedAuthUsers(t)
	mustExec(t, c.admin, `ALTER TABLE auth.users OWNER TO excalibase_app`)
	mustExec(t, c.admin, `CREATE FUNCTION public.tg() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RETURN NEW; END $$`)
	mustExec(t, c.admin, `CREATE TRIGGER planted BEFORE INSERT ON auth.users FOR EACH ROW EXECUTE FUNCTION public.tg()`)
	if !c.probe(t) {
		t.Fatal("the probe did not see a platform table owned by a customer")
	}
	c.register(t)
	if owner := scanString(t, c.admin, `SELECT relowner::regrole::text FROM pg_class WHERE oid = 'auth.users'::regclass`); owner != roleAuthAdmin {
		t.Fatalf("auth.users is owned by %s, want it back with auth_admin", owner)
	}
	if scanBool(t, c.admin, `SELECT EXISTS (SELECT 1 FROM pg_trigger WHERE tgname = 'planted')`) {
		t.Fatal("the trigger on the reclaimed table survived")
	}
	mustExec(t, c.authAdmin, `UPDATE auth.users SET email = 'ada@example.org'`)
	mustBeRefused(t, c.app, `SELECT email FROM auth.users`)
	if c.probe(t) {
		t.Fatal("drift left behind")
	}
}

// Finding 3: a type, an operator (also a prefix one) and a function that already
// exist in the quarantine schema do not stop the move; each goes to a schema
// of its own that the customer did not make.
func TestPlatformSchemas_CollisionsInQuarantineGoToASchemaOfTheirOwn(t *testing.T) {
	c := newPlatformSchemaCase(t)
	c.register(t)
	c.seedAuthUsers(t)
	for _, statement := range []string{
		`CREATE SCHEMA excalibase_quarantine_auth AUTHORIZATION excalibase_app`,
		`CREATE TYPE excalibase_quarantine_auth.mood AS ENUM ('a')`,
		`CREATE TYPE auth.mood AS ENUM ('b')`,
		`ALTER TYPE auth.mood OWNER TO excalibase_app`,
		`CREATE FUNCTION excalibase_quarantine_auth.neg(int) RETURNS int LANGUAGE sql AS 'SELECT 1'`,
		`CREATE FUNCTION auth.neg(int) RETURNS int LANGUAGE sql AS 'SELECT 2'`,
		`ALTER FUNCTION auth.neg(int) OWNER TO excalibase_app`,
		`CREATE OPERATOR excalibase_quarantine_auth.### (RIGHTARG = int, FUNCTION = excalibase_quarantine_auth.neg)`,
		`CREATE OPERATOR auth.### (RIGHTARG = int, FUNCTION = auth.neg)`,
		`ALTER OPERATOR auth.### (NONE, int) OWNER TO excalibase_app`,
	} {
		mustExec(t, c.admin, statement)
	}
	c.register(t)
	if n := scanString(t, c.admin, customerOwnedInPlatformSchemas); n != "0" {
		t.Fatalf("%s customer-owned object(s) are still in the platform schemas", n)
	}
	if scanString(t, c.admin, `SELECT count(DISTINCT n.nspname) FROM pg_namespace n
		WHERE n.nspname LIKE 'excalibase\_quarantine\_auth\_%' AND n.nspowner <> 'excalibase_app'::regrole`) == "0" {
		t.Fatal("the colliding objects did not go to schemas of their own")
	}
	if c.probe(t) {
		t.Fatal("drift left behind")
	}
}

// A range type goes with its multirange, and the project comes out clean.
func TestPlatformSchemas_ARangeTypeAndItsMultirangeAreQuarantined(t *testing.T) {
	c := newPlatformSchemaCase(t)
	c.register(t)
	mustExec(t, c.admin, `CREATE TYPE auth.rg AS RANGE (subtype = int)`)
	mustExec(t, c.admin, `ALTER TYPE auth.rg OWNER TO excalibase_app`)
	if !c.probe(t) {
		t.Fatal("the probe did not see a customer range type")
	}
	c.register(t)
	if n := scanString(t, c.admin, customerOwnedInPlatformSchemas); n != "0" {
		t.Fatalf("%s customer-owned object(s) are still in the platform schemas", n)
	}
	if scanBool(t, c.admin, `SELECT EXISTS (SELECT 1 FROM pg_type WHERE typnamespace = 'auth'::regnamespace AND typname IN ('rg', 'int4multirange', 'rg_multirange'))`) {
		t.Fatalf("the range type or its multirange stayed in auth: %s", scanString(t, c.admin,
			`SELECT string_agg(typname || ':' || typowner::regrole::text || ':' || typtype::text, ', ') FROM pg_type WHERE typnamespace = 'auth'::regnamespace`))
	}
	if c.probe(t) {
		t.Fatal("drift left behind, so the alert would fire every pass")
	}
}

// An owner who fills every schema name the quarantine would try stops the move,
// not the service: the repair ends, refuses and reports.
func TestPlatformSchemas_ExhaustedQuarantineNamesEndInARefusal(t *testing.T) {
	c := newPlatformSchemaCase(t)
	c.register(t)
	mustExec(t, c.admin, `CREATE SCHEMA excalibase_quarantine_auth AUTHORIZATION excalibase_app`)
	mustExec(t, c.admin, `CREATE TYPE excalibase_quarantine_auth.mood AS ENUM ('a')`)
	mustExec(t, c.admin, `CREATE TYPE auth.mood AS ENUM ('b')`)
	mustExec(t, c.admin, `ALTER TYPE auth.mood OWNER TO excalibase_app`)
	oid := scanString(t, c.admin, `SELECT 'auth.mood'::regtype::oid::text`)
	names := []string{"excalibase_quarantine_auth_" + oid}
	for i := 1; i < quarantineNameTries; i++ {
		names = append(names, fmt.Sprintf("excalibase_quarantine_auth_%s_%d", oid, i))
	}
	for _, name := range names {
		mustExec(t, c.admin, `CREATE SCHEMA `+name)
	}
	err := c.registerWith(t, "")
	if err == nil || !strings.Contains(err.Error(), platformSchemaLeftMarker) {
		t.Fatalf("registration did not refuse when no quarantine name was free: %v", err)
	}
	if !scanBool(t, c.admin, `SELECT NOT has_schema_privilege('excalibase_app', 'auth', 'CREATE')`) {
		t.Fatal("the revokes did not commit before the refusal")
	}
}

// The owner can create thousands of roles in a second. The repair costs what the
// grants cost, not roles times objects, so it ends well inside its timeout and
// leaves the project clean.
func TestPlatformSchemas_ThousandsOfCustomerRolesDoNotStopTheRepair(t *testing.T) {
	c := newPlatformSchemaCase(t)
	c.register(t)
	c.seedAuthUsers(t)
	mustExec(t, c.admin, `DO $$ BEGIN FOR i IN 1..2000 LOOP EXECUTE format('CREATE ROLE bulk_%s NOLOGIN', i); END LOOP; END $$`)
	mustExec(t, c.admin, `DO $$ BEGIN FOR i IN 1..300 LOOP EXECUTE format('CREATE TABLE auth.bulk_t%s (id int)', i); END LOOP; END $$`)
	mustExec(t, c.admin, `GRANT USAGE ON SCHEMA auth TO bulk_7; GRANT SELECT ON auth.users TO bulk_1999`)
	plantRoutine(t, c, "auth", "bulk_fn", "bulk_42")
	started := time.Now()
	if err := c.registerWith(t, ""); err != nil {
		t.Fatalf("registration with 2000 customer roles: %v", err)
	}
	if took := time.Since(started); took > 30*time.Second {
		t.Fatalf("the repair took %s with 2000 roles and 300 tables", took)
	}
	if c.probe(t) {
		t.Fatal("drift left behind")
	}
	if scanBool(t, c.admin, `SELECT has_schema_privilege('bulk_7', 'auth', 'USAGE') OR has_table_privilege('bulk_1999', 'auth.users', 'SELECT')`) {
		t.Fatal("a grant to one of the many roles survived")
	}
}

// Each place a customer grant can sit is read for it: the schema, a table, a
// column, a sequence and a function.
func TestPlatformSchemas_EveryKindOfCustomerGrantIsRevoked(t *testing.T) {
	c := newPlatformSchemaCase(t)
	c.register(t)
	c.seedAuthUsers(t)
	for _, statement := range []string{
		`CREATE ROLE g_schema NOLOGIN`, `CREATE ROLE g_table NOLOGIN`, `CREATE ROLE g_column NOLOGIN`,
		`CREATE ROLE g_sequence NOLOGIN`, `CREATE ROLE g_function NOLOGIN`, `CREATE ROLE g_cdc NOLOGIN`,
		`GRANT USAGE ON SCHEMA auth TO g_schema`,
		`GRANT SELECT ON auth.users TO g_table`,
		`GRANT SELECT (email) ON auth.users TO g_column`,
		`GRANT USAGE ON SEQUENCE auth.users_id_seq TO g_sequence`,
		`CREATE FUNCTION auth.plain() RETURNS int LANGUAGE sql AS 'SELECT 1'`,
		`GRANT EXECUTE ON FUNCTION auth.plain() TO g_function`,
		`GRANT USAGE ON SCHEMA excalibase_cdc TO g_cdc`,
	} {
		mustExec(t, c.admin, statement)
	}
	c.register(t)
	for name, check := range map[string]string{
		"schema":   `has_schema_privilege('g_schema', 'auth', 'USAGE')`,
		"table":    `has_table_privilege('g_table', 'auth.users', 'SELECT')`,
		"column":   `has_column_privilege('g_column', 'auth.users', 'email', 'SELECT')`,
		"sequence": `has_sequence_privilege('g_sequence', 'auth.users_id_seq', 'USAGE')`,
		"function": `has_function_privilege('g_function', 'auth.plain()', 'EXECUTE')`,
		"cdc":      `has_schema_privilege('g_cdc', 'excalibase_cdc', 'USAGE')`,
	} {
		if scanBool(t, c.admin, "SELECT "+check) {
			t.Errorf("the %s grant survived", name)
		}
	}
}

// asRoles runs statements as the given roles in one session, each SET ROLE
// taking effect for the statements after it.
func (c *platformSchemaCase) asRoles(t *testing.T, steps ...[2]string) {
	t.Helper()
	tx, err := c.admin.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	for _, step := range steps {
		if _, err := tx.Exec("SET LOCAL ROLE " + step[0]); err != nil {
			t.Fatalf("set role %s: %v", step[0], err)
		}
		if _, err := tx.Exec(step[1]); err != nil {
			t.Fatalf("as %s: %s: %v", step[0], step[1], err)
		}
		if _, err := tx.Exec("RESET ROLE"); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

// A grant passed down a chain with the grant option cannot stop the lockdown
// with "dependent privileges exist": every grant in the chain goes.
func TestPlatformSchemas_AGrantOptionChainIsRevoked(t *testing.T) {
	c := newPlatformSchemaCase(t)
	c.register(t)
	c.seedAuthUsers(t)
	mustExec(t, c.admin, `CREATE ROLE sidekick NOLOGIN; CREATE ROLE sidekick2 NOLOGIN`)
	mustExec(t, c.admin, `GRANT USAGE ON SCHEMA auth TO `+customerOwner+` WITH GRANT OPTION`)
	mustExec(t, c.admin, `GRANT SELECT ON auth.users TO `+customerOwner+` WITH GRANT OPTION`)
	c.asRoles(t,
		[2]string{customerOwner, `GRANT USAGE ON SCHEMA auth TO sidekick WITH GRANT OPTION`},
		[2]string{customerOwner, `GRANT SELECT ON auth.users TO sidekick WITH GRANT OPTION`},
		[2]string{"sidekick", `GRANT USAGE ON SCHEMA auth TO sidekick2`},
		[2]string{"sidekick", `GRANT SELECT ON auth.users TO sidekick2`},
	)
	if !c.probe(t) {
		t.Fatal("the probe did not see the chain")
	}
	c.register(t)
	if scanBool(t, c.admin, `SELECT has_schema_privilege('sidekick2', 'auth', 'USAGE') OR has_table_privilege('sidekick2', 'auth.users', 'SELECT')
		OR has_schema_privilege('sidekick', 'auth', 'USAGE') OR has_table_privilege($1, 'auth.users', 'SELECT')`, customerOwner) {
		t.Fatal("a grant in the chain survived")
	}
}

// Thousands of roles each holding a grant on one table finish inside the timeout.
func TestPlatformSchemas_ThousandsOfGrantHoldersDoNotStopTheRepair(t *testing.T) {
	c := newPlatformSchemaCase(t)
	c.register(t)
	c.seedAuthUsers(t)
	mustExec(t, c.admin, `DO $$ BEGIN FOR i IN 1..300 LOOP EXECUTE format('CREATE TABLE auth.holder_t%s (id int)', i); END LOOP; END $$`)
	mustExec(t, c.admin, `DO $$ BEGIN FOR i IN 1..2000 LOOP
		EXECUTE format('CREATE ROLE holder_%s NOLOGIN', i);
		EXECUTE format('GRANT SELECT ON auth.users TO holder_%s', i);
		EXECUTE format('GRANT USAGE ON SCHEMA auth TO holder_%s', i);
	END LOOP; END $$`)
	started := time.Now()
	if err := c.registerWith(t, ""); err != nil {
		t.Fatalf("registration with 2000 grant holders and 300 tables: %v", err)
	}
	if took := time.Since(started); took > 30*time.Second {
		t.Fatalf("the repair took %s with 2000 grant holders and 300 tables", took)
	}
	if c.probe(t) {
		t.Fatal("drift left behind")
	}
}

// A default privilege that would hand a customer role every new platform table
// is seen and cleared.
func TestPlatformSchemas_DefaultPrivilegesForACustomerAreClearedAndSeen(t *testing.T) {
	c := newPlatformSchemaCase(t)
	c.register(t)
	mustExec(t, c.admin, `ALTER DEFAULT PRIVILEGES FOR ROLE auth_admin IN SCHEMA auth GRANT SELECT ON TABLES TO `+customerOwner)
	mustExec(t, c.admin, `ALTER DEFAULT PRIVILEGES FOR ROLE cdc_watcher IN SCHEMA excalibase_cdc GRANT SELECT ON TABLES TO excalibase_app`)
	if !c.probe(t) {
		t.Fatal("the probe did not see a default privilege for a customer")
	}
	c.register(t)
	if c.probe(t) {
		t.Fatal("the default privileges survived")
	}
	mustExec(t, c.authAdmin, `CREATE TABLE auth.sessions (id int)`)
	if scanBool(t, c.admin, `SELECT has_table_privilege($1, 'auth.sessions', 'SELECT')`, customerOwner) {
		t.Fatal("a table created after the repair is readable by the owner")
	}
}

// A grant on a type or domain in a platform schema is seen and revoked.
func TestPlatformSchemas_TypeAndDomainGrantsToACustomerAreRevoked(t *testing.T) {
	c := newPlatformSchemaCase(t)
	c.register(t)
	mustExec(t, c.authAdmin, `CREATE TYPE auth.platform_mood AS ENUM ('a')`)
	mustExec(t, c.authAdmin, `CREATE DOMAIN auth.platform_pos AS int CHECK (VALUE > 0)`)
	mustExec(t, c.authAdmin, `GRANT USAGE ON TYPE auth.platform_mood TO `+customerOwner)
	mustExec(t, c.authAdmin, `GRANT USAGE ON DOMAIN auth.platform_pos TO `+customerOwner)
	if !c.probe(t) {
		t.Fatal("the probe did not see a grant on a type")
	}
	c.register(t)
	if scanBool(t, c.admin, `SELECT EXISTS (SELECT 1 FROM pg_type t, aclexplode(t.typacl) a
		WHERE t.typnamespace = 'auth'::regnamespace AND a.grantee = $1::regrole)`, customerOwner) {
		t.Fatal("a type or domain grant to the owner survived")
	}
	if c.probe(t) {
		t.Fatal("drift left behind")
	}
}

// An owner who silences notices for the database cannot hide which repair
// script failed: the session pins client_min_messages, so the error still names
// the last script that ran.
func TestPlatformSchemas_AnOwnersMessageLevelDoesNotHideTheFailingScript(t *testing.T) {
	c := newPlatformSchemaCase(t)
	c.register(t)
	mustExec(t, c.owner(t), `ALTER DATABASE app SET client_min_messages = 'error'`)
	mustExec(t, c.owner(t), `ALTER ROLE `+customerOwner+` SET client_min_messages = 'error'`)
	mustExec(t, c.admin, `CREATE TABLE auth.note (id int)`)
	mustExec(t, c.admin, `ALTER TABLE auth.note OWNER TO excalibase_app`)
	err := c.registerWith(t, "")
	if err == nil || !strings.Contains(err.Error(), repairStageMarker+"refusal") {
		t.Fatalf("the failing script is not named in the error: %v", err)
	}
}

// A default privilege with no schema, for a platform role, would make every new
// platform table readable to a customer role; it is seen and cleared.
func TestPlatformSchemas_SchemalessDefaultPrivilegesForACustomerAreClearedAndSeen(t *testing.T) {
	c := newPlatformSchemaCase(t)
	c.register(t)
	mustExec(t, c.admin, `ALTER DEFAULT PRIVILEGES FOR ROLE auth_admin GRANT SELECT ON TABLES TO `+customerOwner)
	mustExec(t, c.admin, `ALTER DEFAULT PRIVILEGES FOR ROLE cdc_watcher GRANT SELECT ON TABLES TO excalibase_app`)
	if !c.probe(t) {
		t.Fatal("the probe did not see a schemaless default privilege for a customer")
	}
	c.register(t)
	if c.probe(t) {
		t.Fatal("the schemaless default privileges survived")
	}
	mustExec(t, c.authAdmin, `CREATE TABLE auth.sessions (id int)`)
	if scanBool(t, c.admin, `SELECT has_table_privilege($1, 'auth.sessions', 'SELECT')`, customerOwner) {
		t.Fatal("a table created after the repair is readable by the owner")
	}
	if !scanBool(t, c.admin, `SELECT EXISTS (SELECT 1 FROM pg_default_acl d, aclexplode(d.defaclacl) a
		WHERE d.defaclnamespace = 'auth'::regnamespace AND a.grantee = 'auth_admin'::regrole)`) {
		t.Fatal("the platform's own default privilege in auth was cleared")
	}
}

// An owner who takes a platform role name makes the registration SQL refuse
// every time; the lockdown and the quarantine run ahead of it and still finish.
func TestPlatformSchemas_ASquattedPlatformRoleDoesNotKeepTheRepairFromRunning(t *testing.T) {
	c := newPlatformSchemaCase(t)
	c.register(t)
	c.seedAuthUsers(t)
	mustExec(t, c.admin, `GRANT documentdb TO `+customerOwner+` WITH ADMIN OPTION`)
	plantRoutine(t, c, "auth", "sq", "excalibase_app")
	mustExec(t, c.admin, `CREATE TABLE auth.note (id int)`)
	mustExec(t, c.admin, `ALTER TABLE auth.note OWNER TO excalibase_app`)
	mustExec(t, c.admin, grantCreateOnAuth+`excalibase_app`)

	err := c.registerWith(t, "")

	if err == nil || !strings.Contains(err.Error(), "were created outside the platform") {
		t.Fatalf("registration did not refuse on the squatted role: %v", err)
	}
	if c.inSchema(t, "auth", "sq") || !c.inAnyQuarantine(t, "sq") {
		t.Fatal("the quarantine did not run ahead of the refusing registration SQL")
	}
	if scanBool(t, c.admin, `SELECT has_schema_privilege('excalibase_app', 'auth', 'CREATE')`) {
		t.Fatal("the lockdown did not run")
	}
	if !c.probe(t) {
		t.Fatal("the table left in auth is not drift, so the reconcile would not alert")
	}
}

// A default privilege granted to PUBLIC, in the schema or for the role anywhere,
// would hand every role each new platform table; it is seen and cleared.
func TestPlatformSchemas_DefaultPrivilegesToPublicAreClearedAndSeen(t *testing.T) {
	for name, statement := range map[string]string{
		"in schema":  `ALTER DEFAULT PRIVILEGES FOR ROLE auth_admin IN SCHEMA auth GRANT SELECT, INSERT ON TABLES TO PUBLIC`,
		"schemaless": `ALTER DEFAULT PRIVILEGES FOR ROLE auth_admin GRANT SELECT, INSERT ON TABLES TO PUBLIC`,
	} {
		t.Run(name, func(t *testing.T) {
			c := newPlatformSchemaCase(t)
			c.register(t)
			mustExec(t, c.admin, statement)
			if !c.probe(t) {
				t.Fatal("the probe did not see a default privilege to PUBLIC")
			}
			c.register(t)
			if c.probe(t) {
				t.Fatal("the default privilege survived")
			}
			mustExec(t, c.authAdmin, `CREATE TABLE auth.tokens (id int)`)
			if scanBool(t, c.admin, `SELECT has_table_privilege($1, 'auth.tokens', 'SELECT') OR has_table_privilege('excalibase_app', 'auth.tokens', 'INSERT')`, customerOwner) {
				t.Fatal("a new platform table is open to a customer role through PUBLIC")
			}
		})
	}
}
