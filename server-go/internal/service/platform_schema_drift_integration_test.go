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

const driftedOwnersProbe = `SELECT coalesce(string_agg(n.nspname || ':' || r.rolname, ',' ORDER BY n.nspname), '')
	FROM pg_namespace n JOIN pg_roles r ON r.oid = n.nspowner
	WHERE n.nspname IN ('auth', 'excalibase_cdc', 'excalibase') AND NOT r.rolsuper`

// A schema handed to a customer role, a grant to one, and what it owns or
// planted in the platform's schemas: the reconcile sees it and puts it back.
func TestPlatformSchemas_ReconcileRepairsDrift(t *testing.T) {
	c := newPlatformSchemaCase(t)
	c.register(t)
	c.seedAuthUsers(t)
	if scanBool(t, c.admin, platformSchemaDriftProbeSQL(customerOwner)) {
		t.Fatal("a freshly registered project reads as drifted")
	}

	for _, statement := range []string{
		`ALTER SCHEMA auth OWNER TO excalibase_app`,
		`ALTER SCHEMA excalibase_cdc OWNER TO excalibase_app`,
		`GRANT CREATE, USAGE ON SCHEMA excalibase_cdc TO ` + customerOwner,
		`GRANT USAGE ON SCHEMA auth TO ` + customerOwner,
		`ALTER TABLE auth.users OWNER TO excalibase_app`,
		`CREATE FUNCTION public.app_trigger() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RETURN NEW; END $$`,
		`ALTER FUNCTION public.app_trigger() OWNER TO excalibase_app`,
		`CREATE TRIGGER planted BEFORE INSERT ON auth.users FOR EACH ROW EXECUTE FUNCTION public.app_trigger()`,
		`CREATE FUNCTION auth.planted() RETURNS int LANGUAGE sql AS 'SELECT 1'`,
		`ALTER FUNCTION auth.planted() OWNER TO excalibase_app`,
	} {
		mustExec(t, c.admin, statement)
	}
	if !scanBool(t, c.admin, platformSchemaDriftProbeSQL(customerOwner)) {
		t.Fatal("the drift probe did not see a schema handed to a customer role")
	}

	c.register(t)

	if scanBool(t, c.admin, platformSchemaDriftProbeSQL(customerOwner)) {
		t.Fatal("the registration SQL left drift behind")
	}
	if drifted := scanString(t, c.admin, driftedOwnersProbe); drifted != "" {
		t.Fatalf("schemas still owned by a non-superuser: %s", drifted)
	}
	for _, check := range []string{
		`SELECT NOT has_schema_privilege('` + customerOwner + `', 'excalibase_cdc', 'CREATE') AND NOT has_schema_privilege('` + customerOwner + `', 'auth', 'USAGE')`,
		`SELECT NOT EXISTS (SELECT 1 FROM pg_trigger WHERE tgname = 'planted')`,
		`SELECT NOT EXISTS (SELECT 1 FROM pg_class c WHERE c.relnamespace = 'auth'::regnamespace AND c.relowner = 'excalibase_app'::regrole)`,
		`SELECT NOT EXISTS (SELECT 1 FROM pg_proc p WHERE p.pronamespace = 'auth'::regnamespace AND p.proowner = 'excalibase_app'::regrole)`,
	} {
		if !scanBool(t, c.admin, check) {
			t.Errorf("not repaired: %s", check)
		}
	}
	// The auth service keeps its table, and the customer still cannot read it.
	mustExec(t, c.authAdmin, `UPDATE auth.users SET email = 'ada@example.org'`)
	mustBeRefused(t, c.app, `SELECT email FROM auth.users`)
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
		"tainted excalibase schema":    `CREATE TABLE excalibase.planted (id int); ALTER TABLE excalibase.planted OWNER TO excalibase_app`,
	} {
		mustExec(t, c.admin, statement)
		if !scanBool(t, c.admin, platformSchemaDriftProbeSQL(customerOwner)) {
			t.Errorf("%s: the probe did not see it", name)
		}
		c.register(t)
		if scanBool(t, c.admin, platformSchemaDriftProbeSQL(customerOwner)) {
			t.Errorf("%s: registration did not put it back", name)
		}
	}
}
