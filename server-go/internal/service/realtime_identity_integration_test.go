//go:build integration

package service

import (
	"database/sql"
	"testing"
)

// The engine withholds a delete whose event lacks the full old row (it cannot
// judge the subscriber's permission on a key alone), so every published table
// must keep REPLICA IDENTITY FULL for as long as it is published.

func TestRealtimeIdentityOnPostgres15(t *testing.T) { realtimeIdentitySuite(t, "postgres:15-alpine") }

func TestRealtimeIdentityOnPostgres17(t *testing.T) { realtimeIdentitySuite(t, "postgres:17-alpine") }

type realtimeIdentityCase struct {
	pg    *rolePostgres
	app   *sql.DB
	admin *sql.DB
	spec  projectRoleSpec
	creds projectRoleCredentials
}

const notesIdentity = "SELECT relreplident::text FROM pg_class WHERE oid = 'public.notes'::regclass"

func realtimeIdentitySuite(t *testing.T, image string) {
	pg := startRolePostgresImage(t, image)
	pg.psql(t, customerClusterSetupSQL())
	c := &realtimeIdentityCase{
		pg:    pg,
		spec:  projectRoleSpec{databaseName: "app", adminUsername: customerOwner},
		creds: projectRoleCredentials{authPassword: "Auth-Password-1", appPassword: customerAppPassword, watcherPassword: "Watcher-Password-1"},
	}
	pg.psql(t, projectRoleSQL(c.spec, c.creds, "cdc_watcher_pub"))
	c.app = pg.connect(t, roleApp, customerAppPassword)
	c.admin = pg.connect(t, "postgres", "p")
	mustExec(t, c.app, "CREATE TABLE public.notes (id serial PRIMARY KEY, owner_id text, body text)")
	mustExec(t, c.app, "SELECT excalibase.set_realtime_table('public', 'notes', true)")

	t.Run("enabling realtime logs complete old rows", c.enablingSetsFull)
	t.Run("a published table cannot drop back to key-only deletes", c.publishedIdentityIsGuarded)
	t.Run("other changes to a published table still work", c.otherAlterationsWork)
	t.Run("an unpublished table may change its identity", c.unpublishedTableIsFree)
	t.Run("registration heals a published table that lost it", c.registrationHeals)
	t.Run("disabling realtime restores the default", c.disablingRestoresDefault)
	t.Run("a routine planted under the realtime function's name cannot stop registration", c.plantedRealtimeRoutine)
}

func (c *realtimeIdentityCase) enablingSetsFull(t *testing.T) {
	if got := scanString(t, c.admin, notesIdentity); got != "f" {
		t.Fatalf("identity = %q, want f", got)
	}
}

func (c *realtimeIdentityCase) publishedIdentityIsGuarded(t *testing.T) {
	for _, statement := range []string{
		"ALTER TABLE public.notes REPLICA IDENTITY DEFAULT",
		"ALTER TABLE public.notes REPLICA IDENTITY USING INDEX notes_pkey",
		"ALTER TABLE public.notes REPLICA IDENTITY NOTHING",
	} {
		mustFail(t, c.app, statement, "realtime")
		mustFail(t, c.admin, statement, "realtime")
	}
	if got := scanString(t, c.admin, notesIdentity); got != "f" {
		t.Fatalf("identity = %q after refused changes, want f", got)
	}
}

func (c *realtimeIdentityCase) otherAlterationsWork(t *testing.T) {
	mustExec(t, c.app, "ALTER TABLE public.notes ADD COLUMN amount int")
	mustExec(t, c.app, "ALTER TABLE public.notes ALTER COLUMN amount TYPE bigint")
	mustExec(t, c.app, "DELETE FROM public.notes")
}

func (c *realtimeIdentityCase) unpublishedTableIsFree(t *testing.T) {
	mustExec(t, c.app, "CREATE TABLE public.drafts (id serial PRIMARY KEY)")
	mustExec(t, c.app, "ALTER TABLE public.drafts REPLICA IDENTITY FULL")
	mustExec(t, c.app, "ALTER TABLE public.drafts REPLICA IDENTITY DEFAULT")
}

func (c *realtimeIdentityCase) registrationHeals(t *testing.T) {
	c.pg.psql(t, "ALTER EVENT TRIGGER excalibase_keep_realtime_identity DISABLE; ALTER TABLE public.notes REPLICA IDENTITY DEFAULT")
	restore := c.spec
	restore.resetPasswords = true
	c.pg.psql(t, projectRoleSQL(restore, c.creds, "cdc_watcher_pub"))
	if got := scanString(t, c.admin, notesIdentity); got != "f" {
		t.Fatalf("identity = %q after registration, want f", got)
	}
	mustFail(t, c.app, "ALTER TABLE public.notes REPLICA IDENTITY DEFAULT", "realtime")
}

func (c *realtimeIdentityCase) disablingRestoresDefault(t *testing.T) {
	mustExec(t, c.app, "SELECT excalibase.set_realtime_table('public', 'notes', false)")
	if got := scanString(t, c.admin, notesIdentity); got != "d" {
		t.Fatalf("identity = %q after disabling, want d", got)
	}
}

func (c *realtimeIdentityCase) plantedRealtimeRoutine(t *testing.T) {
	c.pg.psql(t, "DROP FUNCTION excalibase.set_realtime_table(text, text, boolean)")
	mustFail(t, c.app, "CREATE PROCEDURE excalibase.set_realtime_table(text, text, boolean) LANGUAGE sql AS $$ SELECT 1 $$", "permission denied")
	// What excalibase_app could do before EXC-623.
	c.pg.psql(t, "GRANT CREATE ON SCHEMA excalibase TO excalibase_app")
	mustExec(t, c.app, "CREATE PROCEDURE excalibase.set_realtime_table(text, text, boolean) LANGUAGE sql AS $$ SELECT 1 $$")
	restore := c.spec
	restore.resetPasswords = true
	if err := c.pg.psqlErr(projectRoleSQL(restore, c.creds, "cdc_watcher_pub")); err != nil {
		t.Fatalf("registration failed on a planted routine: %v", err)
	}
	if owner := scanString(t, c.admin, "SELECT proowner::regrole::text FROM pg_proc WHERE oid = 'excalibase.set_realtime_table(text, text, boolean)'::regprocedure"); owner != "postgres" {
		t.Fatalf("set_realtime_table is owned by %q, want postgres", owner)
	}
	mustExec(t, c.app, "SELECT excalibase.set_realtime_table('public', 'notes', true)")
}
