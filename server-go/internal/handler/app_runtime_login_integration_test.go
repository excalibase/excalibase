//go:build integration

package handler

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/edgefn"
	"github.com/excalibase/provisioning-poc/internal/features"
	"github.com/excalibase/provisioning-poc/internal/pgroles"
	"github.com/excalibase/provisioning-poc/internal/pgscram"
)

// EXC-765: apps and functions connect with excalibase_runtime, never the
// owner's login. On Postgres 16 and later the owner holds CREATEROLE, so a
// Developer who deploys code with the owner's login could create or alter
// logins from it. The runtime login does the work an app does and none of that.

const (
	runtimeTestOwner         = "tenant_owner"
	runtimeTestOwnerPassword = "owner-pass"
	runtimeTestPassword      = "runtime-pass"
)

// runtimeProject is a registered project with an owner that already built part
// of its schema before the runtime login existed: what an app deployed before
// EXC-765 left behind.
type runtimeProject struct {
	super   *sql.DB
	logins  map[string]map[string]string
	owner   *sql.DB
	runtime *sql.DB
}

func startRuntimeProject(t *testing.T, image string) *runtimeProject {
	t.Helper()
	super, logins := startProjectPGImage(t, image)
	mustExecE2E(t, super, `CREATE ROLE `+runtimeTestOwner+` LOGIN PASSWORD '`+runtimeTestOwnerPassword+`'`)
	mustExecE2E(t, super, `ALTER DATABASE tenant OWNER TO `+runtimeTestOwner)
	mustExecE2E(t, super, pgroles.CustomerRoleSQL(runtimeTestOwner)+pgroles.OwnerAccessSQL(runtimeTestOwner))
	mustExecE2E(t, super, `GRANT CREATE ON SCHEMA public TO `+runtimeTestOwner)
	owner := openWiringDB(t, map[string]string{"host": logins["auth_admin"]["host"], "port": logins["auth_admin"]["port"],
		"database": "tenant", "username": runtimeTestOwner, "password": runtimeTestOwnerPassword})
	// Built by an app that connected as the owner before this change.
	mustExecE2E(t, owner, `CREATE TABLE public.legacy_orders (id serial PRIMARY KEY, item text);
		INSERT INTO public.legacy_orders (item) VALUES ('before')`)
	// Built through Studio or a platform migration, which run as excalibase_app.
	appDB := openWiringDB(t, logins["excalibase_app"])
	mustExecE2E(t, appDB, `CREATE TABLE public.studio_notes (id serial PRIMARY KEY, body text)`)

	verifier, err := pgscram.Verifier(runtimeTestPassword)
	if err != nil {
		t.Fatal(err)
	}
	// The upgrade: the registration SQL runs again on the live project.
	mustExecE2E(t, super, pgroles.RuntimeRoleSQL(runtimeTestOwner, verifier))
	runtime := openWiringDB(t, map[string]string{"host": logins["auth_admin"]["host"], "port": logins["auth_admin"]["port"],
		"database": "tenant", "username": pgroles.RuntimeRole, "password": runtimeTestPassword})
	return &runtimeProject{super: super, logins: logins, owner: owner, runtime: runtime}
}

func TestRuntimeLogin_CannotManageRolesOnPostgres16(t *testing.T) {
	runtimeCannotManageRoles(t, "postgres:16-alpine")
}

func TestRuntimeLogin_CannotManageRolesOnPostgres15(t *testing.T) {
	runtimeCannotManageRoles(t, "postgres:15-alpine")
}

func runtimeCannotManageRoles(t *testing.T, image string) {
	p := startRuntimeProject(t, image)

	// The attack is real: on Postgres 16 and later the owner's own login creates a role.
	if strings.Contains(image, "postgres:16") {
		if _, err := p.owner.Exec(`CREATE ROLE control_made LOGIN PASSWORD 'x'`); err != nil {
			t.Fatalf("control: the owner cannot create a role, so the attack below proves nothing: %v", err)
		}
	}

	var createRole, superuser bool
	if err := p.super.QueryRow(`SELECT rolcreaterole, rolsuper FROM pg_roles WHERE rolname = $1`, pgroles.RuntimeRole).Scan(&createRole, &superuser); err != nil {
		t.Fatal(err)
	}
	if createRole || superuser {
		t.Fatalf("the runtime login has createrole=%v superuser=%v", createRole, superuser)
	}

	refused := []string{
		`CREATE ROLE planted LOGIN PASSWORD 'x'`,
		`ALTER ROLE ` + runtimeTestOwner + ` PASSWORD 'taken'`,
		`ALTER ROLE ` + pgroles.RuntimeRole + ` CREATEROLE`,
		`ALTER ROLE ` + pgroles.RuntimeRole + ` SUPERUSER`,
		`DROP ROLE ` + runtimeTestOwner,
		`GRANT ` + runtimeTestOwner + ` TO ` + pgroles.RuntimeRole + ` WITH ADMIN OPTION`,
		`SELECT excalibase.create_role('planted', 'x', true)`,
		`SELECT excalibase.alter_role_password('` + runtimeTestOwner + `', 'taken')`,
		`SELECT excalibase.drop_role('anyone')`,
		// Becoming the owner or the platform role, then calling a role function as it.
		`SET ROLE ` + runtimeTestOwner + `; SELECT excalibase.create_role('planted', 'x', true)`,
		`SET ROLE ` + runtimeTestOwner + `; CREATE ROLE planted2`,
		`SET ROLE excalibase_app; SELECT excalibase.create_role('planted', 'x', true)`,
		`SELECT password FROM auth.users`,
		`CREATE TABLE excalibase.planted (id int)`,
		`COPY (SELECT 1) TO PROGRAM 'id'`,
	}
	for _, statement := range refused {
		conn, err := p.runtime.Conn(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		_, err = conn.ExecContext(context.Background(), statement)
		_ = conn.Close()
		if err == nil {
			t.Errorf("the runtime login ran: %s", statement)
		}
	}
	var planted int
	if err := p.super.QueryRow(`SELECT count(*) FROM pg_roles WHERE rolname LIKE 'planted%'`).Scan(&planted); err != nil || planted != 0 {
		t.Fatalf("a role was planted: %d, %v", planted, err)
	}

	// The owner's own path is unchanged: Studio still manages roles as the owner.
	if _, err := p.owner.Exec(`SELECT excalibase.create_role('studio_made', 'x', false)`); err != nil {
		t.Errorf("the owner can no longer create a role: %v", err)
	}
}

// What an app does: reads and writes, sequences, and migrations of its own
// tables, including the ones built before the runtime login existed.
func TestRuntimeLogin_AppStillWorksItsOwnTables(t *testing.T) {
	p := startRuntimeProject(t, "postgres:16-alpine")
	for _, statement := range []string{
		`INSERT INTO public.legacy_orders (item) VALUES ('after')`,
		`UPDATE public.legacy_orders SET item = 'changed' WHERE item = 'before'`,
		`ALTER TABLE public.legacy_orders ADD COLUMN qty int`,
		`CREATE INDEX legacy_orders_item ON public.legacy_orders (item)`,
		`INSERT INTO public.studio_notes (body) VALUES ('hello')`,
		`CREATE TABLE public.fresh (id serial PRIMARY KEY, label text)`,
		`INSERT INTO public.fresh (label) VALUES ('a')`,
		`CREATE SCHEMA billing`,
		`CREATE TABLE billing.invoices (id serial PRIMARY KEY, total numeric)`,
		`INSERT INTO billing.invoices (total) VALUES (10)`,
		`ALTER TABLE public.fresh ADD COLUMN extra text`,
		`DROP TABLE public.fresh`,
	} {
		if _, err := p.runtime.Exec(statement); err != nil {
			t.Errorf("%s: %v", statement, err)
		}
	}
	var item string
	if err := p.runtime.QueryRow(`SELECT item FROM public.legacy_orders ORDER BY id LIMIT 1`).Scan(&item); err != nil || item != "changed" {
		t.Fatalf("legacy_orders first row = %q, %v", item, err)
	}

	// What the app makes, the owner and the platform role can still read.
	mustExecE2E(t, p.runtime, `CREATE TABLE public.shared_by_app (id serial PRIMARY KEY, label text)`)
	mustExecE2E(t, p.runtime, `INSERT INTO public.shared_by_app (label) VALUES ('x')`)
	for who, db := range map[string]*sql.DB{"owner": p.owner, "excalibase_app": openWiringDB(t, p.logins["excalibase_app"])} {
		var label string
		if err := db.QueryRow(`SELECT label FROM public.shared_by_app`).Scan(&label); err != nil || label != "x" {
			t.Errorf("%s reads what the app made: %q, %v", who, label, err)
		}
	}
}

// Re-running the registration SQL is idempotent and takes the password the
// platform files, so a reconcile or a restore leaves the login as it was.
func TestRuntimeLogin_RegistrationSQLIsIdempotentAndResetsThePassword(t *testing.T) {
	p := startRuntimeProject(t, "postgres:16-alpine")
	verifier, err := pgscram.Verifier("second-pass")
	if err != nil {
		t.Fatal(err)
	}
	mustExecE2E(t, p.super, pgroles.RuntimeRoleSQL(runtimeTestOwner, verifier))
	mustExecE2E(t, p.super, `ALTER ROLE `+pgroles.RuntimeRole+` PASSWORD '`+verifier+`'`)
	mustExecE2E(t, p.super, pgroles.RuntimeRoleSQL(runtimeTestOwner, verifier))
	again := openWiringDB(t, map[string]string{"host": p.logins["auth_admin"]["host"], "port": p.logins["auth_admin"]["port"],
		"database": "tenant", "username": pgroles.RuntimeRole, "password": "second-pass"})
	if _, err := again.Exec(`INSERT INTO public.legacy_orders (item) VALUES ('still works')`); err != nil {
		t.Fatalf("after a second run: %v", err)
	}
	mustExecE2E(t, p.super, pgroles.RuntimeLockSQL())
	dsn := fmt.Sprintf("host=%s port=%s dbname=tenant user=%s password=%s sslmode=disable",
		p.logins["auth_admin"]["host"], p.logins["auth_admin"]["port"], pgroles.RuntimeRole, "second-pass")
	locked, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer locked.Close()
	if err := locked.Ping(); err == nil {
		t.Fatal("a locked runtime login signed in")
	}
}

// A function's EXCALIBASE_DB_URL is the runtime login, and what it can do is
// what an app can do: its URL cannot mint a login.
func TestFunctionDBURL_IsTheRuntimeLoginAndCannotMintALogin(t *testing.T) {
	p := startRuntimeProject(t, "postgres:16-alpine")
	app := p.logins["excalibase_app"]
	v := newFakeVault()
	v.data["projects/"+wiringProjectID+"/credentials/admin"] = map[string]string{
		"host": app["host"], "port": app["port"], "database": "tenant", "username": runtimeTestOwner, "password": runtimeTestOwnerPassword,
	}
	v.data["projects/"+wiringProjectID+"/credentials/excalibase_runtime"] = map[string]string{
		"host": app["host"], "port": app["port"], "database": "tenant", "username": pgroles.RuntimeRole, "password": runtimeTestPassword,
	}
	h := NewFunctionHandler(edgefn.NewFunctionStore(t.TempDir()), edgefn.NewSecretsStore(v), nil,
		&inMemoryInstanceStore{insts: map[string]*domain.DatabaseInstance{wiringProjectID: {ProjectID: wiringProjectID, OrgID: "default"}}},
		nil, testAPIBase)
	h.SetVault(v)
	h.SetFeatures(features.NewStatic(features.AppRuntimeLogin))
	url := h.builtinEnv(context.Background(), wiringProjectID)["EXCALIBASE_DB_URL"]
	if !strings.Contains(url, pgroles.RuntimeRole+":") || strings.Contains(url, runtimeTestOwner) || strings.Contains(url, runtimeTestOwnerPassword) {
		t.Fatalf("the function's database URL is not the runtime login: %q", url)
	}
	function, err := sql.Open("postgres", strings.Replace(url, "sslmode=require", "sslmode=disable", 1))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = function.Close() })
	mustExecE2E(t, function, `CREATE TABLE public.notes (id int)`)
	for _, statement := range []string{
		`CREATE ROLE minted LOGIN PASSWORD 'x'`,
		`SELECT excalibase.create_role('minted', 'x', true)`,
		`SELECT password FROM auth.users`,
	} {
		if _, err := function.Exec(statement); err == nil {
			t.Errorf("the function's login ran %s", statement)
		}
	}
}

// EXC-765 review: code the runtime login plants (a trigger, a function, a
// default) runs as whichever session fires it. When the owner's session fires
// it, the role functions must still refuse, whichever way they are reached.
func TestRuntimeLogin_PlantedCodeCannotMintARoleThroughTheOwnersSession(t *testing.T) {
	for _, image := range []string{"postgres:16-alpine", "postgres:15-alpine"} {
		t.Run(image, func(t *testing.T) {
			p := startRuntimeProject(t, image)
			for _, statement := range []string{
				`CREATE FUNCTION public.mint() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN PERFORM excalibase.create_role('trig_made', 'x', true); RETURN NEW; END $$`,
				`CREATE TRIGGER mint BEFORE INSERT ON public.legacy_orders FOR EACH ROW EXECUTE FUNCTION public.mint()`,
				`CREATE FUNCTION public.mint_now() RETURNS int LANGUAGE plpgsql AS $$ BEGIN PERFORM excalibase.create_role('func_made', 'x', true); RETURN 1; END $$`,
				`CREATE FUNCTION public.mint_sql() RETURNS int LANGUAGE sql AS $$ SELECT 1 FROM (SELECT excalibase.create_role('sql_made', 'x', true)) s $$`,
			} {
				mustExecE2E(t, p.runtime, statement)
			}
			mustExecE2E(t, p.super, `GRANT EXECUTE ON FUNCTION public.mint_now(), public.mint_sql() TO PUBLIC`)
			for _, fired := range []string{
				`INSERT INTO public.legacy_orders (item) VALUES ('fires the trigger')`,
				`SELECT public.mint_now()`,
				`SELECT public.mint_sql()`,
				`DO $$ BEGIN PERFORM excalibase.create_role('do_made', 'x', true); END $$`,
			} {
				if _, err := p.owner.Exec(fired); err == nil {
					t.Errorf("the owner's session ran %s without the role function refusing", fired)
				}
			}
			var minted int
			if err := p.super.QueryRow(`SELECT count(*) FROM pg_roles WHERE rolname IN ('trig_made', 'func_made', 'sql_made', 'do_made')`).Scan(&minted); err != nil || minted != 0 {
				t.Fatalf("planted code minted %d role(s): %v", minted, err)
			}
			// What the owner types itself still works.
			mustExecE2E(t, p.owner, `SELECT excalibase.create_role('typed_by_owner', 'x', false)`)
		})
	}
}
