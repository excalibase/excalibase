//go:build integration

package service

import (
	"database/sql"
	"strings"
	"testing"
)

// EXC-821: the schemas the platform manages stay the platform's. Customer SQL
// (excalibase_app) and the owner's login can neither change them nor take them,
// and a reconcile puts back what drifted.
var platformManagedSchemas = []string{"excalibase", "auth", "excalibase_cdc"}

func TestPlatformSchemas_CustomerRolesCannotChangeThem(t *testing.T) {
	c := newPlatformSchemaCase(t)
	c.register(t)
	c.seedAuthUsers(t)
	roles := map[string]*sql.DB{roleApp: c.app, customerOwner: c.owner(t)}
	for role, db := range roles {
		for _, schema := range platformManagedSchemas {
			for _, statement := range []string{
				"CREATE TABLE " + schema + ".planted (id int)",
				"CREATE FUNCTION " + schema + ".planted() RETURNS int LANGUAGE sql AS 'SELECT 1'",
				"ALTER SCHEMA " + schema + " OWNER TO " + role,
				"ALTER SCHEMA " + schema + " RENAME TO planted_schema",
				"DROP SCHEMA " + schema + " CASCADE",
			} {
				if _, err := db.Exec(statement); err == nil {
					t.Errorf("%s was allowed to run: %s", role, statement)
				} else if !strings.Contains(err.Error(), "permission denied") && !strings.Contains(err.Error(), "must be owner") {
					t.Errorf("%s: %s refused for another reason: %v", role, statement, err)
				}
			}
		}
		mustBeRefused(t, db, "ALTER TABLE auth.users OWNER TO "+role)
		// Postgres only warns on a GRANT by a role with no grant option; what
		// counts is that nothing was granted.
		for _, schema := range platformManagedSchemas {
			_, _ = db.Exec("GRANT ALL ON SCHEMA " + schema + " TO PUBLIC")
			_, _ = db.Exec("GRANT ALL ON SCHEMA " + schema + " TO " + role)
			if scanBool(t, c.admin, "SELECT has_schema_privilege('public', $1, 'CREATE') OR has_schema_privilege($2, $1, 'CREATE')", schema, role) {
				t.Errorf("%s granted itself or PUBLIC CREATE on %s", role, schema)
			}
		}
		_, _ = db.Exec("GRANT ALL ON TABLE auth.users TO " + role)
		if scanBool(t, c.admin, "SELECT has_table_privilege($1, 'auth.users', 'SELECT')", role) {
			t.Errorf("%s granted itself SELECT on auth.users", role)
		}
	}
}

// customerOwnedInPlatformSchemas counts the objects of a customer role (any role
// that is not a superuser and not the platform's) left in auth or excalibase_cdc.
const customerOwnedInPlatformSchemas = `
SELECT count(*) FROM (
  SELECT c.relowner AS owner, c.relnamespace AS ns FROM pg_class c
  UNION ALL SELECT p.proowner, p.pronamespace FROM pg_proc p
  UNION ALL SELECT t.typowner, t.typnamespace FROM pg_type t WHERE t.typrelid = 0 AND t.typcategory <> 'A'
  UNION ALL SELECT o.oprowner, o.oprnamespace FROM pg_operator o
) objects JOIN pg_roles r ON r.oid = objects.owner
WHERE objects.ns IN ('auth'::regnamespace, 'excalibase_cdc'::regnamespace)
  AND NOT r.rolsuper AND r.rolname NOT IN ('auth_admin', 'cdc_watcher')`

func (c *platformSchemaCase) probe(t *testing.T) bool {
	t.Helper()
	return scanBool(t, c.admin, platformSchemaDriftProbeSQL())
}

// EXC-821 review, finding 1 (critical): a SECURITY DEFINER function a customer
// planted in auth was handed to auth_admin, so a customer view over it read
// auth.users. A customer's object is moved out, still the customer's, and
// gains nothing. The same for types, domains, operators and a table with a
// rule, default and policy, and for roles beyond the owner.
func TestPlatformSchemas_CustomerObjectsAreQuarantinedNotAdopted(t *testing.T) {
	c := newPlatformSchemaCase(t)
	c.register(t)
	c.seedAuthUsers(t)
	for _, statement := range []string{
		`CREATE ROLE cust_extra NOLOGIN`,
		`CREATE FUNCTION auth.leak() RETURNS SETOF text LANGUAGE sql SECURITY DEFINER AS 'SELECT email FROM auth.users'`,
		`ALTER FUNCTION auth.leak() OWNER TO excalibase_app`,
		`CREATE VIEW public.leak AS SELECT * FROM auth.leak()`,
		`ALTER VIEW public.leak OWNER TO excalibase_app`,
		`CREATE FUNCTION excalibase_cdc.held() RETURNS int LANGUAGE sql AS 'SELECT 1'`,
		`ALTER FUNCTION excalibase_cdc.held() OWNER TO cust_extra`,
		`CREATE TYPE auth.mood AS ENUM ('ok')`,
		`ALTER TYPE auth.mood OWNER TO excalibase_app`,
		`CREATE DOMAIN auth.positive AS int CHECK (VALUE > 0)`,
		`ALTER DOMAIN auth.positive OWNER TO ` + customerOwner,
		`CREATE FUNCTION auth.opfn(int, int) RETURNS int LANGUAGE sql AS 'SELECT 1'`,
		`CREATE OPERATOR auth.### (LEFTARG = int, RIGHTARG = int, FUNCTION = auth.opfn)`,
		`ALTER OPERATOR auth.### (int, int) OWNER TO excalibase_app`,
		`ALTER FUNCTION auth.opfn(int, int) OWNER TO excalibase_app`,
		`CREATE TABLE auth.planted (id int DEFAULT 1)`,
		`ALTER TABLE auth.planted OWNER TO ` + customerOwner,
		`CREATE RULE planted_rule AS ON INSERT TO auth.planted DO ALSO NOTHING`,
		`GRANT USAGE ON SCHEMA auth TO cust_extra`,
		`GRANT SELECT ON auth.users TO cust_extra`,
	} {
		mustExec(t, c.admin, statement)
	}
	if !c.probe(t) {
		t.Fatal("the probe did not see customer objects in the platform schemas")
	}

	c.register(t)

	if n := scanString(t, c.admin, customerOwnedInPlatformSchemas); n != "0" {
		t.Fatalf("%s customer-owned object(s) are still in the platform schemas", n)
	}
	if owner := scanString(t, c.admin, `SELECT proowner::regrole::text FROM pg_proc WHERE proname = 'leak' AND pronamespace = 'excalibase_quarantine_auth'::regnamespace`); owner != roleApp {
		t.Fatalf("the planted function is owned by %q in quarantine, want it still excalibase_app's", owner)
	}
	// The view still exists but the function runs as its own customer owner now.
	mustBeRefused(t, c.app, `SELECT * FROM public.leak`)
	for _, check := range []string{
		`SELECT NOT EXISTS (SELECT 1 FROM pg_proc WHERE pronamespace IN ('auth'::regnamespace, 'excalibase_cdc'::regnamespace) AND has_function_privilege('public', oid, 'EXECUTE'))`,
		`SELECT NOT has_schema_privilege('cust_extra', 'auth', 'USAGE') AND NOT has_table_privilege('cust_extra', 'auth.users', 'SELECT')`,
		`SELECT NOT has_schema_privilege('excalibase_app', 'auth', 'USAGE') AND NOT has_schema_privilege('` + customerOwner + `', 'excalibase_cdc', 'USAGE')`,
	} {
		if !scanBool(t, c.admin, check) {
			t.Errorf("not repaired: %s", check)
		}
	}
	if c.probe(t) {
		t.Fatal("the registration SQL left drift behind")
	}
	// The auth service is unharmed.
	mustExec(t, c.authAdmin, `UPDATE auth.users SET email = 'ada@example.org'`)
	mustExec(t, c.authAdmin, `CREATE TABLE auth.sessions (id int)`)
}

// Finding 2 (critical): a trigger on a platform table survived because the
// routine behind it was moved first. A trigger the platform did not put there is
// dropped whoever owns its function.
func TestPlatformSchemas_PlantedTriggerOnAPlatformTableIsDropped(t *testing.T) {
	c := newPlatformSchemaCase(t)
	c.register(t)
	c.seedAuthUsers(t)
	for _, statement := range []string{
		`CREATE FUNCTION auth.trig() RETURNS trigger LANGUAGE plpgsql SECURITY DEFINER AS $$ BEGIN RETURN NEW; END $$`,
		`ALTER FUNCTION auth.trig() OWNER TO excalibase_app`,
		`CREATE TRIGGER planted_in_schema BEFORE INSERT ON auth.users FOR EACH ROW EXECUTE FUNCTION auth.trig()`,
		`CREATE FUNCTION public.trig() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RETURN NEW; END $$`,
		`ALTER FUNCTION public.trig() OWNER TO excalibase_app`,
		`CREATE TRIGGER planted_in_public BEFORE UPDATE ON auth.users FOR EACH ROW EXECUTE FUNCTION public.trig()`,
	} {
		mustExec(t, c.admin, statement)
	}
	if !c.probe(t) {
		t.Fatal("the probe did not see a trigger on a platform table")
	}
	c.register(t)
	if scanBool(t, c.admin, `SELECT EXISTS (SELECT 1 FROM pg_trigger WHERE tgname LIKE 'planted_in_%')`) {
		t.Fatal("a planted trigger survived")
	}
	if c.probe(t) {
		t.Fatal("the registration SQL left drift behind")
	}
}

// Finding 3 (high): a rule on a platform table is dropped; a policy or default
// that calls a routine the platform does not own is not adopted, the repair
// refuses and leaves the table for an operator.
func TestPlatformSchemas_RulesAreDroppedAndTaintedTablesAreRefused(t *testing.T) {
	c := newPlatformSchemaCase(t)
	c.register(t)
	c.seedAuthUsers(t)
	mustExec(t, c.admin, `CREATE FUNCTION public.evil() RETURNS boolean LANGUAGE sql AS 'SELECT true'`)
	mustExec(t, c.admin, `ALTER FUNCTION public.evil() OWNER TO excalibase_app`)

	mustExec(t, c.admin, `CREATE RULE planted_rule AS ON INSERT TO auth.users DO ALSO NOTHING`)
	c.register(t)
	if scanBool(t, c.admin, `SELECT EXISTS (SELECT 1 FROM pg_rewrite WHERE rulename = 'planted_rule')`) {
		t.Fatal("a planted rule survived")
	}

	for name, statement := range map[string]string{
		"policy":  `CREATE POLICY planted ON auth.users USING (public.evil())`,
		"default": `ALTER TABLE auth.users ADD COLUMN planted boolean DEFAULT public.evil()`,
		"check":   `ALTER TABLE auth.users ADD CONSTRAINT planted CHECK (public.evil())`,
	} {
		mustExec(t, c.admin, statement)
		if !c.probe(t) {
			t.Errorf("%s: the probe did not see it", name)
		}
		err := c.pg.psqlErr(mustProjectRoleSQL(t, c.spec, c.creds, "cdc_watcher_pub"))
		if err == nil || !strings.Contains(err.Error(), "left for an operator") {
			t.Errorf("%s: registration did not refuse the table: %v", name, err)
		}
		mustExec(t, c.admin, map[string]string{
			"policy": `DROP POLICY planted ON auth.users`, "default": `ALTER TABLE auth.users DROP COLUMN planted`,
			"check": `ALTER TABLE auth.users DROP CONSTRAINT planted`}[name])
	}
}

// A password changed in SQL by the role itself is not stopped, and the
// reconcile finds it by comparing the stored verifier with the vault's
// password and sets it back.
func TestPlatformSchemas_SelfPasswordChangeIsFoundAndRepaired(t *testing.T) {
	c := newPlatformSchemaCase(t)
	c.register(t)
	mustExec(t, c.app, `ALTER ROLE excalibase_app PASSWORD 'changed-in-sql'`)
	mustExec(t, c.owner(t), `ALTER ROLE `+customerOwner+` PASSWORD 'owner-changed-in-sql'`)

	want := []RolePassword{
		{Role: roleApp, Password: customerAppPassword},
		{Role: customerOwner, Password: customerOwnerPassword},
	}
	drift := passwordDrift(c.storedVerifiers(t, roleApp, customerOwner), want)
	if len(drift) != 2 {
		t.Fatalf("drift = %+v, want both roles", drift)
	}
	repair, err := passwordRepairSQL(drift)
	if err != nil {
		t.Fatal(err)
	}
	c.pg.psql(t, repair)

	if again := passwordDrift(c.storedVerifiers(t, roleApp, customerOwner), want); len(again) != 0 {
		t.Fatalf("still drifted after the repair: %+v", again)
	}
	c.pg.connect(t, roleApp, customerAppPassword)
	c.pg.connect(t, customerOwner, customerOwnerPassword)
}

// A login the platform switched off is not switched back on by a repair.
func TestPlatformSchemas_PasswordRepairKeepsALoginSwitchedOff(t *testing.T) {
	c := newPlatformSchemaCase(t)
	c.register(t)
	mustExec(t, c.admin, `CREATE ROLE switched_off NOLOGIN`)
	repair, err := passwordRepairSQL([]RolePassword{{Role: "switched_off", Password: "Vault-Password-1"}})
	if err != nil {
		t.Fatal(err)
	}
	c.pg.psql(t, repair)
	if scanBool(t, c.admin, `SELECT rolcanlogin FROM pg_roles WHERE rolname = 'switched_off'`) {
		t.Fatal("the password repair switched a login back on")
	}
}

func (c *platformSchemaCase) storedVerifiers(t *testing.T, roles ...string) []storedRoleVerifier {
	t.Helper()
	var stored []storedRoleVerifier
	for _, role := range roles {
		stored = append(stored, storedRoleVerifier{
			Role:     role,
			Verifier: scanString(t, c.admin, `SELECT coalesce(rolpassword, '') FROM pg_authid WHERE rolname = $1`, role),
		})
	}
	return stored
}

// Each kind of drift alone is seen by the probe and put back by registration.
func TestPlatformSchemas_EachKindOfDriftIsSeenAndRepaired(t *testing.T) {
	c := newPlatformSchemaCase(t)
	c.register(t)
	c.seedAuthUsers(t)
	mustExec(t, c.admin, `CREATE FUNCTION public.app_trigger() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RETURN NEW; END $$`)
	mustExec(t, c.admin, `ALTER FUNCTION public.app_trigger() OWNER TO excalibase_app`)
	for name, statement := range map[string]string{
		"create on the watcher schema": `GRANT CREATE ON SCHEMA excalibase_cdc TO ` + customerOwner,
		"usage on auth":                `GRANT USAGE ON SCHEMA auth TO excalibase_app`,
		"customer-owned schema":        `ALTER SCHEMA excalibase_cdc OWNER TO ` + customerOwner,
		"customer-owned function":      `CREATE FUNCTION auth.planted() RETURNS int LANGUAGE sql AS 'SELECT 1'; ALTER FUNCTION auth.planted() OWNER TO excalibase_app`,
		"customer function trigger":    `CREATE TRIGGER planted BEFORE INSERT ON auth.users FOR EACH ROW EXECUTE FUNCTION public.app_trigger()`,
		"customer-owned table":         `CREATE TABLE excalibase_cdc.planted (id int); ALTER TABLE excalibase_cdc.planted OWNER TO ` + customerOwner,
		"customer-owned type":          `CREATE TYPE auth.planted_type AS (one int); ALTER TYPE auth.planted_type OWNER TO excalibase_app`,
		"any other customer role":      `CREATE ROLE cust_extra NOLOGIN; GRANT USAGE ON SCHEMA auth TO cust_extra`,
		"public may execute":           `CREATE FUNCTION auth.helper() RETURNS int LANGUAGE sql AS 'SELECT 1'`,
		"tainted excalibase schema":    `CREATE TABLE excalibase.planted (id int); ALTER TABLE excalibase.planted OWNER TO excalibase_app`,
	} {
		mustExec(t, c.admin, statement)
		if !c.probe(t) {
			t.Errorf("%s: the probe did not see it", name)
		}
		c.register(t)
		if c.probe(t) {
			t.Errorf("%s: registration did not put it back", name)
		}
	}
}
