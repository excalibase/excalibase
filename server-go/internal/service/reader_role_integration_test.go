//go:build integration

package service

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"net/url"
	"strings"
	"testing"
	"time"

	tcexec "github.com/testcontainers/testcontainers-go/exec"

	"github.com/excalibase/provisioning-poc/internal/pgroles"
)

// EXC-675 on real Postgres: the reader reads every user table, now and
// later, writes nothing (not even DDL in public on 14), and a rotation's
// session scripts end what the role held, on the primary and on a standby.

const readerPassword = "Reader-Password-1"

func TestReaderRoleOnPostgres14(t *testing.T) { readerRoleSuite(t, "postgres:14-alpine") }

func TestReaderRoleOnPostgres17(t *testing.T) { readerRoleSuite(t, "postgres:17-alpine") }

type readerCase struct {
	pg     *rolePostgres
	owner  *sql.DB
	app    *sql.DB
	admin  *sql.DB
	reader *sql.DB
	spec   projectRoleSpec
	creds  projectRoleCredentials
}

func readerRoleSuite(t *testing.T, image string) {
	pg := startRolePostgresImage(t, image)
	pg.psql(t, customerClusterSetupSQL())
	pg.psql(t, "CREATE TABLE public.before_registration (id int); INSERT INTO public.before_registration VALUES (1); ALTER TABLE public.before_registration OWNER TO "+customerOwner)
	c := &readerCase{
		pg:   pg,
		spec: projectRoleSpec{databaseName: "app", adminUsername: customerOwner},
		creds: projectRoleCredentials{authPassword: "Auth-Password-1", appPassword: customerAppPassword,
			watcherPassword: "Watcher-Password-1", readerPassword: readerPassword},
	}
	if err := runSuperuserRoleSQL(context.Background(), pg.execer(), "app", reservedSchemaFirstSQL(), projectRoleSQL(c.spec, c.creds, "cdc_watcher_pub")); err != nil {
		t.Fatalf("registration: %v", err)
	}
	c.owner = pg.connect(t, customerOwner, customerOwnerPassword)
	c.app = pg.connect(t, roleApp, customerAppPassword)
	c.admin = pg.connect(t, "postgres", "p")
	c.reader = pg.connect(t, roleReader, readerPassword)

	t.Run("reads a table that predates it", c.readsExistingTable)
	t.Run("reads tables the app and the owner create later", c.readsLaterTables)
	t.Run("reads a schema the app or the owner creates later", c.readsLaterSchemas)
	t.Run("writes nothing", c.writesNothing)
	t.Run("changes no schema even with read-only turned off", c.changesNoSchema)
	t.Run("creates no large object", c.createsNoLargeObject)
	t.Run("signs in to no other database", c.reachesNoOtherDatabase)
	t.Run("reaches no platform schema", c.reachesNoPlatformSchema)
	t.Run("holds no privileged attribute", func(t *testing.T) { expectPlainRole(t, c.admin, roleReader) })
	t.Run("re-registration keeps it reading", c.reRegistrationKeepsIt)
	t.Run("a copy without the feature cannot sign in as it", c.unfiledReaderIsLocked)
}

func (c *readerCase) readsExistingTable(t *testing.T) {
	if got := scanString(t, c.reader, "SELECT count(*)::text FROM public.before_registration"); got != "1" {
		t.Fatalf("read %q", got)
	}
}

func (c *readerCase) readsLaterTables(t *testing.T) {
	mustExec(t, c.app, "CREATE TABLE public.teams (id serial PRIMARY KEY, name text)")
	mustExec(t, c.app, "INSERT INTO public.teams (name) VALUES ('Leeds')")
	mustExec(t, c.owner, "CREATE TABLE public.owned (id int)")
	mustExec(t, c.owner, "INSERT INTO public.owned VALUES (7)")
	mustExec(t, c.app, "CREATE VIEW public.team_names AS SELECT name FROM public.teams")
	for query, want := range map[string]string{
		"SELECT name FROM public.teams":             "Leeds",
		"SELECT id::text FROM public.owned":         "7",
		"SELECT name FROM public.team_names":        "Leeds",
		"SELECT last_value::text FROM teams_id_seq": "1",
	} {
		if got := scanString(t, c.reader, query); got != want {
			t.Errorf("%s = %q, want %q", query, got, want)
		}
	}
}

func (c *readerCase) readsLaterSchemas(t *testing.T) {
	mustExec(t, c.app, "CREATE SCHEMA league")
	mustExec(t, c.app, "CREATE TABLE league.matches (id int)")
	mustExec(t, c.app, "INSERT INTO league.matches VALUES (3)")
	mustExec(t, c.owner, "CREATE SCHEMA private_owner")
	mustExec(t, c.owner, "CREATE TABLE private_owner.notes (body text)")
	mustExec(t, c.owner, "INSERT INTO private_owner.notes VALUES ('hi')")
	if got := scanString(t, c.reader, "SELECT id::text FROM league.matches"); got != "3" {
		t.Errorf("league.matches = %q", got)
	}
	if got := scanString(t, c.reader, "SELECT body FROM private_owner.notes"); got != "hi" {
		t.Errorf("private_owner.notes = %q", got)
	}
}

func (c *readerCase) writesNothing(t *testing.T) {
	mustFail(t, c.reader, "INSERT INTO public.teams (name) VALUES ('x')", "read-only transaction")
	conn := readWriteConn(t, c.reader)
	for _, statement := range []string{
		"INSERT INTO public.teams (name) VALUES ('x')",
		"UPDATE public.owned SET id = 8",
		"DELETE FROM league.matches",
		"TRUNCATE public.owned",
		"SELECT nextval('teams_id_seq')",
	} {
		if _, err := conn.ExecContext(context.Background(), statement); err == nil || !strings.Contains(err.Error(), "permission denied") {
			t.Errorf("%s: %v", statement, err)
		}
	}
}

// readWriteConn is one reader session that turned read-only off, as a client
// that wanted to write would.
func readWriteConn(t *testing.T, db *sql.DB) *sql.Conn {
	t.Helper()
	conn := heldConn(t, db)
	if _, err := conn.ExecContext(context.Background(), "SET default_transaction_read_only = off"); err != nil {
		t.Fatalf("set: %v", err)
	}
	return conn
}

func (c *readerCase) changesNoSchema(t *testing.T) {
	conn := readWriteConn(t, c.reader)
	for _, statement := range []string{
		"CREATE TABLE public.planted (id int)",
		"CREATE FUNCTION public.planted() RETURNS int LANGUAGE sql AS 'SELECT 1'",
		"CREATE SCHEMA planted",
		"ALTER TABLE public.teams ADD COLUMN planted int",
	} {
		if _, err := conn.ExecContext(context.Background(), statement); err == nil {
			t.Errorf("the reader ran %s", statement)
		}
	}
	if got := scanString(t, c.admin, "SELECT count(*)::text FROM pg_class WHERE relname = 'planted'"); got != "0" {
		t.Error("the reader left a relation behind")
	}
}

// A large object is written to disk whatever the transaction mode; the roles
// that build the schema keep creating them.
func (c *readerCase) createsNoLargeObject(t *testing.T) {
	conn := readWriteConn(t, c.reader)
	for _, statement := range []string{
		"SELECT lo_from_bytea(0, 'planted')",
		"SELECT lo_creat(-1)",
		"SELECT lo_create(0)",
	} {
		if _, err := conn.ExecContext(context.Background(), statement); err == nil || !strings.Contains(err.Error(), "permission denied") {
			t.Errorf("%s: %v", statement, err)
		}
	}
	if got := scanString(t, c.admin, "SELECT count(*)::text FROM pg_largeobject_metadata WHERE lomowner = '"+roleReader+"'::regrole"); got != "0" {
		t.Errorf("the reader owns %s large object(s)", got)
	}
	for name, db := range map[string]*sql.DB{"owner": c.owner, "app": c.app} {
		if _, err := db.Exec("SELECT lo_unlink(lo_from_bytea(0, 'kept'))"); err != nil {
			t.Errorf("%s can no longer create a large object: %v", name, err)
		}
	}
}

// The DDL trigger lives in the project's database only, so the reader must
// not sign in to postgres or template1, where it could plant objects (and
// template1 would copy them into every new database).
func (c *readerCase) reachesNoOtherDatabase(t *testing.T) {
	if err := c.pg.connectDirect(t, roleReader, readerPassword, "postgres").Ping(); err != nil {
		t.Fatalf("precondition: the image's own pg_hba should admit the reader anywhere: %v", err)
	}
	c.pg.prependHBA(t, pgroles.ReaderHBA("host", "app"))
	if err := c.pg.connectDirect(t, roleReader, readerPassword, "app").Ping(); err != nil {
		t.Fatalf("the reader lost its own database: %v", err)
	}
	for _, database := range []string{"postgres", "template1"} {
		err := c.pg.connectDirect(t, roleReader, readerPassword, database).Ping()
		if err == nil || !strings.Contains(err.Error(), "pg_hba.conf rejects") {
			t.Errorf("the reader signs in to %s: %v", database, err)
		}
	}
	if err := c.pg.connectDirect(t, customerOwner, customerOwnerPassword, "postgres").Ping(); err != nil {
		t.Errorf("the owner lost the maintenance database: %v", err)
	}
}

// prependHBA puts lines first in the server's pg_hba, in order, and reloads it.
func (p *rolePostgres) prependHBA(t *testing.T, lines []string) {
	t.Helper()
	script := `hba=$(psql -U postgres -d postgres -tAc 'SHOW hba_file') && { printf '%s\n' "$LINES"; cat "$hba"; } > /tmp/hba && cat /tmp/hba > "$hba" && psql -U postgres -d postgres -tAc 'SELECT pg_reload_conf()'`
	p.mustRun(t, []string{"sh", "-c", script}, "LINES="+strings.Join(lines, "\n"))
}

// connectDirect dials the container's own address: the image trusts loopback,
// and a mapped port can arrive from it, so only this proves pg_hba's verdict.
func (p *rolePostgres) connectDirect(t *testing.T, user, password, database string) *sql.DB {
	t.Helper()
	ip, err := p.container.ContainerIP(context.Background())
	if err != nil {
		t.Fatalf("container ip: %v", err)
	}
	dsn := url.URL{
		Scheme:   "postgres",
		User:     url.UserPassword(user, password),
		Host:     ip + ":5432",
		Path:     database,
		RawQuery: "sslmode=disable",
	}
	db, err := sql.Open("postgres", dsn.String())
	if err != nil {
		t.Fatalf("open as %s: %v", user, err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func (c *readerCase) reachesNoPlatformSchema(t *testing.T) {
	c.pg.psql(t, "CREATE TABLE IF NOT EXISTS auth.users (id int, password_hash text)")
	c.pg.psql(t, "CREATE TABLE excalibase.job_runs (id int)")
	mustFail(t, c.reader, "SELECT * FROM auth.users", "permission denied")
	mustFail(t, c.reader, "SELECT * FROM excalibase.job_runs", "permission denied")
	mustFail(t, c.reader, "SELECT * FROM excalibase.excalibase_cron_jobs", "permission denied")
}

func (c *readerCase) reRegistrationKeepsIt(t *testing.T) {
	restore := c.spec
	restore.resetPasswords = true
	if err := runSuperuserRoleSQL(context.Background(), c.pg.execer(), "app", reservedSchemaFirstSQL(), projectRoleSQL(restore, c.creds, "cdc_watcher_pub")); err != nil {
		t.Fatalf("re-registration: %v", err)
	}
	c.pg.assertLogin(t, roleReader, readerPassword)
	if got := scanString(t, c.reader, "SELECT name FROM public.teams"); got != "Leeds" {
		t.Errorf("read %q after re-registration", got)
	}
}

func (c *readerCase) unfiledReaderIsLocked(t *testing.T) {
	withoutReader := c.creds
	withoutReader.readerPassword = ""
	restore := c.spec
	restore.resetPasswords = true
	if err := runSuperuserRoleSQL(context.Background(), c.pg.execer(), "app", reservedSchemaFirstSQL(), projectRoleSQL(restore, withoutReader, "cdc_watcher_pub")); err != nil {
		t.Fatalf("registration without a reader: %v", err)
	}
	fresh := c.pg.connect(t, roleReader, readerPassword)
	if err := fresh.Ping(); err == nil || !strings.Contains(err.Error(), "not permitted to log in") {
		t.Fatalf("the source's reader still signs in: %v", err)
	}
}

// A session the role opened before the change ends; the caller's own and
// other roles' sessions stay.
func TestTerminateRoleSessionsEndsOnlyThatRole(t *testing.T) {
	pg := startRolePostgresImage(t, "postgres:16-alpine")
	pg.psql(t, "CREATE ROLE held LOGIN PASSWORD 'Held-1'; CREATE ROLE other LOGIN PASSWORD 'Other-1'")
	held := heldConn(t, pg.connect(t, "held", "Held-1"))
	other := heldConn(t, pg.connect(t, "other", "Other-1"))

	if _, err := pg.execOutputStdin(sessionPsql, terminateRoleSessionsSQL("held")); err != nil {
		t.Fatalf("terminate: %v", err)
	}
	if err := held.PingContext(context.Background()); err == nil {
		t.Fatal("the held session survived")
	}
	if err := other.PingContext(context.Background()); err != nil {
		t.Fatalf("another role's session was ended: %v", err)
	}
}

// On a standby, the script waits until it has replayed past the change and
// then ends the role's sessions there; a position it never reaches fails.
func TestAwaitReplayThenTerminateOnAStandby(t *testing.T) {
	pg := startRolePostgresImage(t, "postgres:16-alpine")
	pg.psql(t, "CREATE ROLE held LOGIN PASSWORD 'Held-1'")
	pg.startStandby(t)
	pg.mustRun(t, []string{"sh", "-c", "nohup su-exec postgres psql -p 5433 -U held -d app -c 'SELECT pg_sleep(120)' >/dev/null 2>&1 &"}, "PGPASSWORD=Held-1")
	waitForStandbySession(t, pg, "held")

	pg.psql(t, "ALTER ROLE held PASSWORD 'Held-2'")
	position := strings.TrimSpace(pg.mustRun(t, []string{"psql", "-X", "-tA", "-U", "postgres", "-c", "SELECT pg_current_wal_lsn()"}, ""))
	if !walPosition.MatchString(position) {
		t.Fatalf("position %q", position)
	}
	standbyPsql := append([]string{}, sessionPsql...)
	standbyPsql = append(standbyPsql[:1], append([]string{"-p", "5433"}, standbyPsql[1:]...)...)
	if _, err := pg.execOutputStdin(standbyPsql, awaitReplaySQL(position)+terminateRoleSessionsSQL("held")); err != nil {
		t.Fatalf("standby script: %v", err)
	}
	if got := standbySessions(t, pg, "held"); got != "0" {
		t.Fatalf("the standby kept %s session(s)", got)
	}
	if _, err := pg.execOutputStdin(standbyPsql, strings.Replace(awaitReplaySQL("FF/0"), "1..60", "1..2", 1)); err == nil {
		t.Fatal("a position the standby never reached passed")
	}
}

func heldConn(t *testing.T, db *sql.DB) *sql.Conn {
	t.Helper()
	conn, err := db.Conn(context.Background())
	if err != nil {
		t.Fatalf("conn: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	if err := conn.PingContext(context.Background()); err != nil {
		t.Fatalf("ping: %v", err)
	}
	return conn
}

// startStandby streams a hot standby from the primary inside the same
// container, on port 5433.
func (p *rolePostgres) startStandby(t *testing.T) {
	t.Helper()
	p.mustRun(t, []string{"su-exec", "postgres", "pg_basebackup", "-h", "/var/run/postgresql", "-U", "postgres", "-D", "/tmp/standby", "-R", "-X", "stream"}, "")
	p.mustRun(t, []string{"su-exec", "postgres", "pg_ctl", "-D", "/tmp/standby", "-o", "-p 5433", "-l", "/tmp/standby.log", "-w", "start"}, "")
}

func standbySessions(t *testing.T, p *rolePostgres, role string) string {
	t.Helper()
	return strings.TrimSpace(p.mustRun(t, []string{"psql", "-X", "-tA", "-p", "5433", "-U", "postgres", "-c",
		"SELECT count(*) FROM pg_stat_activity WHERE usename = '" + role + "'"}, ""))
}

func waitForStandbySession(t *testing.T, p *rolePostgres, role string) {
	t.Helper()
	for i := 0; i < 50; i++ {
		if standbySessions(t, p, role) == "1" {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("no %s session opened on the standby", role)
}

// mustRun runs argv in the container, with env as KEY=VALUE when given.
func (p *rolePostgres) mustRun(t *testing.T, argv []string, env string) string {
	t.Helper()
	options := []tcexec.ProcessOption{tcexec.Multiplexed()}
	if env != "" {
		options = append(options, tcexec.WithEnv([]string{env}))
	}
	code, out, err := p.container.Exec(context.Background(), argv, options...)
	if err != nil {
		t.Fatalf("exec %v: %v", argv, err)
	}
	body := readAll(out)
	if code != 0 {
		t.Fatalf("exec %v exit %d: %s", argv, code, body)
	}
	return body
}

// execOutputStdin runs argv with stdin, as provisioning does through a pod or container.
func (p *rolePostgres) execOutputStdin(argv []string, stdin string) (string, error) {
	shell := []string{"sh", "-c", `printf '%s' "$SCRIPT" | ` + strings.Join(quoteArgs(argv), " ")}
	code, out, err := p.container.Exec(context.Background(), shell, tcexec.Multiplexed(), tcexec.WithEnv([]string{"SCRIPT=" + stdin}))
	if err != nil {
		return "", err
	}
	body := readAll(out)
	if code != 0 {
		return body, &execFailure{code: code, output: body}
	}
	return body, nil
}

func readAll(out io.Reader) string {
	body, _ := io.ReadAll(out)
	return string(body)
}

func quoteArgs(argv []string) []string {
	quoted := make([]string, 0, len(argv))
	for _, arg := range argv {
		quoted = append(quoted, "'"+strings.ReplaceAll(arg, "'", `'\''`)+"'")
	}
	return quoted
}

type execFailure struct {
	code   int
	output string
}

func (e *execFailure) Error() string { return fmt.Sprintf("exit %d: %s", e.code, e.output) }
