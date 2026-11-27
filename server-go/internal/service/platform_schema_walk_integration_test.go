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

// A customer table that inherits a platform table has its triggers fire when the
// platform updates the parent, as the platform's role. Inheritance is recorded
// child to parent, so the walk from the table never sees it: the repair detaches
// the child (it keeps its rows and columns) and the probe sees the link.
func TestPlatformSchemas_ACustomerChildOfAPlatformTableIsDetached(t *testing.T) {
	c := newPlatformSchemaCase(t)
	c.register(t)
	c.seedAuthUsers(t)
	c.plantEvil(t)
	mustExec(t, c.admin, `CREATE TABLE public.child () INHERITS (auth.users)`)
	mustExec(t, c.admin, `ALTER TABLE public.child OWNER TO excalibase_app`)
	mustExec(t, c.app, `CREATE FUNCTION public.evil_tg() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN PERFORM public.evil(); RETURN NEW; END $$`)
	mustExec(t, c.app, `CREATE TRIGGER planted BEFORE UPDATE ON public.child FOR EACH ROW EXECUTE FUNCTION public.evil_tg()`)
	mustExec(t, c.app, `INSERT INTO public.child (email) VALUES ('kid@example.org')`)
	mustExec(t, c.admin, `ALTER TABLE auth.users OWNER TO auth_admin`)
	if !c.probe(t) {
		t.Fatal("the probe did not see a customer child of a platform table")
	}
	c.register(t)
	if c.probe(t) {
		t.Fatal("the child is still attached after the repair")
	}
	mustExec(t, c.authAdmin, `UPDATE auth.users SET email = email`)
	if got := c.loot(t); got != "" {
		t.Fatalf("customer code ran as %q", got)
	}
	if !scanBool(t, c.admin, `SELECT EXISTS (SELECT 1 FROM public.child WHERE email = 'kid@example.org')`) {
		t.Fatal("the detached child lost its rows")
	}
}

// The same holds for a customer partition of a partitioned platform table.
func TestPlatformSchemas_ACustomerPartitionOfAPlatformTableIsDetached(t *testing.T) {
	c := newPlatformSchemaCase(t)
	c.register(t)
	mustExec(t, c.admin, `CREATE TABLE auth.parted (id int PRIMARY KEY) PARTITION BY RANGE (id)`)
	mustExec(t, c.admin, `CREATE TABLE public.part PARTITION OF auth.parted FOR VALUES FROM (0) TO (10)`)
	mustExec(t, c.admin, `ALTER TABLE public.part OWNER TO excalibase_app`)
	if !c.probe(t) {
		t.Fatal("the probe did not see a customer partition of a platform table")
	}
	c.register(t)
	if c.probe(t) || scanBool(t, c.admin, `SELECT EXISTS (SELECT 1 FROM pg_inherits)`) {
		t.Fatal("the partition is still attached after the repair")
	}
}

// A customer table that references a platform table (the common profiles table
// with ON DELETE CASCADE) is no taint: the internal triggers on the platform
// table depend on the customer's constraint, but the cascade runs as the
// referencing table's owner. A foreign key from a platform table to a customer
// table stays tainted (the walk test above).
func TestPlatformSchemas_ACustomerForeignKeyIntoAPlatformTableIsClean(t *testing.T) {
	c := newPlatformSchemaCase(t)
	c.register(t)
	c.seedAuthUsers(t)
	mustExec(t, c.admin, `CREATE TABLE public.profiles (user_id int REFERENCES auth.users(id) ON DELETE CASCADE)`)
	mustExec(t, c.admin, `ALTER TABLE public.profiles OWNER TO excalibase_app`)
	if c.probe(t) {
		t.Fatal("a customer profiles table makes the platform schema read as tainted")
	}
	c.register(t)
	if !scanBool(t, c.admin, `SELECT EXISTS (SELECT 1 FROM pg_constraint WHERE conrelid = 'public.profiles'::regclass AND contype = 'f')`) {
		t.Fatal("the repair removed the customer's foreign key")
	}
}

// A customer foreign key into a platform table is clean only when the check it
// runs uses built-in types and operators: a customer type with an implicit cast
// to integer makes Postgres run the customer's cast as the platform table's owner.
func TestPlatformSchemas_AForeignKeyThroughACustomerCastIsTainted(t *testing.T) {
	c := newPlatformSchemaCase(t)
	c.register(t)
	c.seedAuthUsers(t)
	c.plantEvil(t)
	mustExec(t, c.admin, `ALTER TABLE auth.users OWNER TO auth_admin`)
	for _, statement := range []string{
		`CREATE TYPE public.uid AS (v int)`,
		`CREATE FUNCTION public.uid_to_int(public.uid) RETURNS integer LANGUAGE plpgsql AS $$ BEGIN PERFORM public.evil(); RETURN ($1).v; END $$`,
		`CREATE CAST (public.uid AS integer) WITH FUNCTION public.uid_to_int(public.uid) AS IMPLICIT`,
	} {
		mustExec(t, c.app, statement)
	}
	mustExec(t, c.admin, `CREATE TABLE public.prof (u public.uid REFERENCES auth.users(id))`)
	mustExec(t, c.admin, `ALTER TABLE public.prof OWNER TO excalibase_app`)
	if !c.probe(t) {
		t.Fatal("the probe did not see a foreign key that runs a customer cast")
	}
	err := c.registerWith(t, "")
	if err == nil || !strings.Contains(err.Error(), platformSchemaLeftMarker) {
		t.Fatalf("registration did not leave the table for an operator: %v", err)
	}
}
