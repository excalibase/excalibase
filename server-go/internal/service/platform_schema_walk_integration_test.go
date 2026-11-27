//go:build integration

package service

import (
	"os"
	"strings"
	"testing"
)

// EXC-821: whatever a platform table depends on must be the platform's, found by
// walking pg_depend from the table and everything attached to it, not by naming
// the kinds that carry code. Each case starts with a customer-owned auth.users
// and a customer function evil() that records who ran it.

func (c *platformSchemaCase) plantEvil(t *testing.T) {
	t.Helper()
	mustExec(t, c.admin, `ALTER TABLE auth.users OWNER TO excalibase_app`)
	for _, statement := range []string{
		`CREATE TABLE public.loot (who text)`,
		`GRANT INSERT, SELECT ON public.loot TO PUBLIC`,
		`CREATE FUNCTION public.evil() RETURNS boolean LANGUAGE plpgsql AS $$ BEGIN INSERT INTO public.loot VALUES (current_user); RETURN true; END $$`,
	} {
		mustExec(t, c.app, statement)
	}
}

func (c *platformSchemaCase) loot(t *testing.T) string {
	t.Helper()
	return scanString(t, c.admin, `SELECT coalesce(string_agg(who, ','), '') FROM public.loot`)
}

// A trigger whose WHEN clause calls customer code is dropped although its
// function is the platform's.
func TestPlatformSchemas_ATriggerWhenClauseCallingCustomerCodeIsDropped(t *testing.T) {
	c := newPlatformSchemaCase(t)
	c.register(t)
	c.seedAuthUsers(t)
	c.plantEvil(t)
	mustExec(t, c.admin, `CREATE TRIGGER planted BEFORE UPDATE ON auth.users FOR EACH ROW WHEN (public.evil()) EXECUTE FUNCTION suppress_redundant_updates_trigger()`)
	if !c.probe(t) {
		t.Fatal("the probe did not see the trigger")
	}
	c.register(t)
	if scanBool(t, c.admin, `SELECT EXISTS (SELECT 1 FROM pg_trigger WHERE tgname = 'planted')`) {
		t.Fatal("the trigger survived")
	}
	mustExec(t, c.authAdmin, `UPDATE auth.users SET email = 'x@example.org'`)
	if got := c.loot(t); got != "" {
		t.Fatalf("customer code ran as %q", got)
	}
	if c.probe(t) {
		t.Fatal("drift left behind")
	}
}

// A forced policy that reads a customer view is dropped, and FORCE is cleared on
// the table taken back.
func TestPlatformSchemas_AForcedPolicyReadingACustomerViewIsDropped(t *testing.T) {
	c := newPlatformSchemaCase(t)
	c.register(t)
	c.seedAuthUsers(t)
	c.plantEvil(t)
	for _, statement := range []string{
		`CREATE VIEW public.v AS SELECT public.evil() AS ok`,
		`GRANT SELECT ON public.v TO PUBLIC`,
		`ALTER TABLE auth.users ENABLE ROW LEVEL SECURITY`,
		`ALTER TABLE auth.users FORCE ROW LEVEL SECURITY`,
		`CREATE POLICY planted ON auth.users USING (EXISTS (SELECT 1 FROM public.v))`,
	} {
		mustExec(t, c.admin, statement)
	}
	if !c.probe(t) {
		t.Fatal("the probe did not see the policy")
	}
	c.register(t)
	if scanBool(t, c.admin, `SELECT EXISTS (SELECT 1 FROM pg_policy WHERE polname = 'planted')`) {
		t.Fatal("the policy survived")
	}
	if scanBool(t, c.admin, `SELECT relforcerowsecurity FROM pg_class WHERE oid = 'auth.users'::regclass`) {
		t.Fatal("FORCE ROW LEVEL SECURITY is still on")
	}
	mustExec(t, c.authAdmin, `SELECT count(*) FROM auth.users`)
	if got := c.loot(t); got != "" {
		t.Fatalf("customer code ran as %q", got)
	}
	if c.probe(t) {
		t.Fatal("drift left behind")
	}
}

// Dependencies that cannot be dropped are left for an operator: the probe sees
// them and registration refuses. A column of a customer domain, a foreign key to
// a customer table, and a customer publication that includes a platform table.
func TestPlatformSchemas_DependenciesOnCustomerObjectsAreLeftForAnOperator(t *testing.T) {
	for name, setup := range map[string][]string{
		"column of a customer domain": {
			`CREATE DOMAIN public.evildom AS text CHECK (public.evil())`, `ALTER DOMAIN public.evildom OWNER TO excalibase_app`,
			`ALTER TABLE auth.users ADD COLUMN note public.evildom`},
		"foreign key to a customer table": {
			`CREATE TABLE public.cust (id int PRIMARY KEY)`, `ALTER TABLE public.cust OWNER TO excalibase_app`,
			`ALTER TABLE auth.users ADD COLUMN cust_id int REFERENCES public.cust(id)`},
		"customer publication": {
			`CREATE PUBLICATION cust_pub FOR TABLE auth.users`, `ALTER PUBLICATION cust_pub OWNER TO excalibase_app`},
		"customer publication of the whole schema": {
			`CREATE PUBLICATION cust_ns_pub FOR TABLES IN SCHEMA auth`, `ALTER PUBLICATION cust_ns_pub OWNER TO excalibase_app`},
	} {
		t.Run(name, func(t *testing.T) {
			c := newPlatformSchemaCase(t)
			c.register(t)
			c.seedAuthUsers(t)
			c.plantEvil(t)
			for _, statement := range setup {
				mustExec(t, c.admin, statement)
			}
			if !c.probe(t) {
				t.Fatal("the probe did not see it")
			}
			err := c.registerWith(t, "")
			if err == nil || !strings.Contains(err.Error(), platformSchemaLeftMarker) {
				t.Fatalf("registration did not leave the table for an operator: %v", err)
			}
		})
	}
}

// The auth service's own migrations depend on nothing foreign: a table made by
// them is never tainted.
func TestPlatformSchemas_TheAuthMigrationsAreClean(t *testing.T) {
	c := newPlatformSchemaCase(t)
	c.register(t)
	migrations, err := os.ReadFile("testdata/auth_migrations_postgres.sql")
	if err != nil {
		t.Fatal(err)
	}
	mustExec(t, c.authAdmin, string(migrations))
	c.register(t)
	if c.probe(t) {
		t.Fatal("the platform's own tables read as tainted")
	}
}

// An object that belongs to an extension a superuser installed is the platform's:
// a default calling its function does not taint the table.
func TestPlatformSchemas_AFunctionOfASuperuserExtensionIsNotForeign(t *testing.T) {
	c := newPlatformSchemaCase(t)
	c.register(t)
	c.seedAuthUsers(t)
	mustExec(t, c.admin, `CREATE EXTENSION IF NOT EXISTS pgcrypto`)
	mustExec(t, c.admin, `ALTER TABLE auth.users ADD COLUMN token text DEFAULT encode(gen_random_bytes(4), 'hex')`)
	if c.probe(t) {
		t.Fatal("a default calling an extension function reads as tainted")
	}
	c.register(t)
}
