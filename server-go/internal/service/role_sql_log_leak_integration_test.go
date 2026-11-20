//go:build integration

package service

import (
	"context"
	"database/sql"
	"encoding/hex"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/provisioner"
	tcexec "github.com/testcontainers/testcontainers-go/exec"
)

// Postgres logs a failed statement in full, and a project's Viewer reads that
// log on the Logs page, in get_logs and through a log drain, so a password must
// never reach the server in any form that decodes back to it.

func logLeakCreds() projectRoleCredentials {
	return projectRoleCredentials{
		authPassword:      generatePassword(32),
		appPassword:       generatePassword(32),
		watcherPassword:   generatePassword(32),
		schedulerPassword: generatePassword(32),
		readerPassword:    generatePassword(32),
	}
}

func (c projectRoleCredentials) byRole() map[string]string {
	return map[string]string{
		roleAuthAdmin: c.authPassword,
		roleApp:       c.appPassword,
		roleWatcher:   c.watcherPassword,
		roleScheduler: c.schedulerPassword,
		roleReader:    c.readerPassword,
	}
}

// assertNoVerifier: a verifier cannot sign in, but it can be attacked offline
// and proves nothing is logged at all.
func assertNoVerifier(t *testing.T, where, text string) {
	t.Helper()
	if strings.Contains(text, "SCRAM-SHA-256$") || strings.Contains(strings.ToLower(text), hex.EncodeToString([]byte("SCRAM-SHA-256$"))) {
		t.Errorf("%s holds a SCRAM verifier", where)
	}
}

func (p *rolePostgres) serverLog(t *testing.T) string {
	t.Helper()
	reader, err := p.container.Logs(context.Background())
	if err != nil {
		t.Fatalf("read the server log: %v", err)
	}
	defer reader.Close()
	body, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("read the server log: %v", err)
	}
	return string(body)
}

// waitForStatementLogging returns once the server logs a plain statement, so a
// clean log afterwards means something.
func (p *rolePostgres) waitForStatementLogging(t *testing.T) {
	t.Helper()
	const marker = "excalibase_logging_marker"
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		p.psql(t, "SELECT '"+marker+"'")
		if strings.Contains(p.serverLog(t), marker) {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatal("statement logging did not come on, so this test proves nothing")
}

// recordingExecer keeps every argv, which the exec URL carries into the API
// server's audit log and the engine's exec record.
func recordingExecer(exec roleSQLExecer, argv *[]string) roleSQLExecer {
	return func(ctx context.Context, cmd []string, stdin string) (string, error) {
		*argv = append(*argv, strings.Join(cmd, " "))
		return exec(ctx, cmd, stdin)
	}
}

func logLeakImages(t *testing.T, run func(t *testing.T, start func(*testing.T) *rolePostgres)) {
	for _, image := range []string{"postgres:14", "postgres:17", "postgres:18"} {
		t.Run(image, func(t *testing.T) {
			run(t, func(t *testing.T) *rolePostgres { return startRolePostgresImage(t, image) })
		})
	}
	t.Run("CNPG operand 17", func(t *testing.T) {
		run(t, func(t *testing.T) *rolePostgres { return startCNPGRolePostgres(t, "17") })
	})
}

func TestRoleSQLFailureLeavesNoPasswordInTheServerLog(t *testing.T) {
	logLeakImages(t, func(t *testing.T, start func(*testing.T) *rolePostgres) {
		pg := start(t)
		c := newPlatformSchemaCaseOn(t, pg)
		creds := logLeakCreds()
		spec := c.spec
		spec.resetPasswords = true

		var argv []string
		script := mustProjectRoleSQL(t, spec, creds, "cdc_watcher_pub") + "\nSELECT 1/0;\n"
		err := runSuperuserRoleSQL(context.Background(), recordingExecer(pg.execer(), &argv), "app", reservedSchemaFirstSQL(), script)
		if err == nil {
			t.Fatal("the forced failure did not fail")
		}
		log := pg.serverLog(t)
		if !strings.Contains(log, "division by zero") {
			t.Fatal("the server log does not hold the forced failure, so this test proves nothing")
		}
		assertNoPassword(t, "server log", log, creds.byRole())
		assertNoPassword(t, "returned error", err.Error(), creds.byRole())
		assertNoPassword(t, "exec arguments", strings.Join(argv, "\n"), creds.byRole())
		assertNoVerifier(t, "server log", log)
		assertNoVerifier(t, "returned error", err.Error())
		assertNoVerifier(t, "exec arguments", strings.Join(argv, "\n"))
	})
}

// A failure inside the block that names the verifier: EXECUTE's CONTEXT quotes
// the statement, to the server log and to the client, whose error becomes the
// project's failure reason.
func TestRoleSQLFailureInTheSettingBlockNamesNothing(t *testing.T) {
	logLeakImages(t, func(t *testing.T, start func(*testing.T) *rolePostgres) {
		pg := start(t)
		password := generatePassword(32)
		verifiers := mustRoleVerifiers(t, []RolePassword{{Role: "no_such_role", Password: password}})
		err := runSuperuserRoleSQL(context.Background(), pg.execer(), "app", BuildProjectRoleResetSQL(verifiers))
		if err == nil {
			t.Fatal("altering a role that does not exist did not fail")
		}
		if !strings.Contains(err.Error(), "no_such_role") {
			t.Fatalf("the error lost its cause: %v", err)
		}
		passwords := map[string]string{"no_such_role": password}
		log := pg.serverLog(t)
		assertNoPassword(t, "server log", log, passwords)
		assertNoVerifier(t, "server log", log)
		assertNoPassword(t, "returned error", err.Error(), passwords)
		assertNoVerifier(t, "returned error", err.Error())
	})
}

// An operator who turned statement logging on must not get the role SQL in it,
// on a run that succeeds as much as on one that fails.
func TestRoleSQLStaysOutOfALogThatRecordsEveryStatement(t *testing.T) {
	logLeakImages(t, func(t *testing.T, start func(*testing.T) *rolePostgres) {
		pg := start(t)
		c := newPlatformSchemaCaseOn(t, pg)
		pg.psql(t, "ALTER SYSTEM SET log_statement = 'all'")
		pg.psql(t, "ALTER SYSTEM SET log_min_duration_statement = 0")
		pg.psql(t, "SELECT pg_reload_conf()")
		pg.waitForStatementLogging(t)
		creds := logLeakCreds()
		spec := c.spec
		spec.resetPasswords = true
		script := mustProjectRoleSQL(t, spec, creds, "cdc_watcher_pub")
		if err := runSuperuserRoleSQL(context.Background(), pg.execer(), "app", reservedSchemaFirstSQL(), script); err != nil {
			t.Fatalf("role SQL: %v", err)
		}
		pg.waitForStatementLogging(t)
		log := pg.serverLog(t)
		assertNoPassword(t, "server log", log, creds.byRole())
		assertNoVerifier(t, "server log", log)
		for role, password := range creds.byRole() {
			var current string
			if err := pg.connect(t, role, password).QueryRow("SELECT current_user").Scan(&current); err != nil {
				t.Errorf("%s cannot sign in with its filed password: %v", role, err)
			}
		}
	})
}

// The path the orchestration expects to take: a customer holds a lock, the
// script fails on lock_timeout, the holder is terminated and the retry lands,
// so what the failed attempt logged are the passwords the project keeps. One
// holder stops a GRANT, the other the very block that names a verifier.
func TestRoleSQLLockTimeoutRetryLeavesNoPasswordInTheServerLog(t *testing.T) {
	for name, holder := range map[string]struct {
		statement  string
		resetAdmin bool
	}{
		"a table's catalog row": {statement: "ALTER TABLE public.held ADD COLUMN extra int"},
		"the owner's own role":  {statement: "ALTER ROLE " + customerOwner + " PASSWORD 'Owner-Self-Held-1'", resetAdmin: true},
	} {
		t.Run(name, func(t *testing.T) {
			logLeakImages(t, func(t *testing.T, start func(*testing.T) *rolePostgres) {
				pg := start(t)
				c := newPlatformSchemaCaseOn(t, pg)
				c.registerAsBeforeEXC623(t)
				// Customer SQL runs as excalibase_app; the owner can only hold its own row.
				session := pg.connect(t, roleApp, customerAppPassword)
				mustExec(t, session, "CREATE TABLE public.held (id int)")
				if holder.resetAdmin {
					session = pg.connect(t, customerOwner, customerOwnerPassword)
				}
				tx, err := session.Begin()
				if err != nil {
					t.Fatal(err)
				}
				defer func() { _ = tx.Rollback() }()
				if _, err := tx.Exec(holder.statement); err != nil {
					t.Fatal(err)
				}

				creds := logLeakCreds()
				spec := c.spec
				spec.resetPasswords, spec.resetAdmin = true, holder.resetAdmin
				spec.adminPassword = generatePassword(32)
				passwords := creds.byRole()
				passwords[customerOwner] = spec.adminPassword

				var argv []string
				err = runSuperuserRoleSQL(context.Background(), recordingExecer(pg.execer(), &argv), "app",
					reservedSchemaFirstSQL(), mustProjectRoleSQL(t, spec, creds, "cdc_watcher_pub"))
				if err != nil {
					t.Fatalf("the retry did not land: %v", err)
				}
				log := pg.serverLog(t)
				if !strings.Contains(log, "canceling statement due to lock timeout") {
					t.Fatal("no lock timeout in the server log, so this test proves nothing")
				}
				assertNoPassword(t, "server log", log, passwords)
				assertNoVerifier(t, "server log", log)
				assertNoPassword(t, "exec arguments", strings.Join(argv, "\n"), passwords)
				assertNoVerifier(t, "exec arguments", strings.Join(argv, "\n"))
				for role, password := range creds.byRole() {
					var current string
					if err := pg.connect(t, role, password).QueryRow("SELECT current_user").Scan(&current); err != nil {
						t.Errorf("%s cannot sign in with its filed password: %v", role, err)
					}
				}
			})
		})
	}
}

// rotationOn drives the production rotation statement through the real exec
// stream, on a session as plain as production's.
func rotationOn(t *testing.T, pg *rolePostgres) (*ProvisioningService, *domain.DatabaseInstance) {
	t.Helper()
	docker, err := provisioner.NewRealDockerClient(provisioner.DockerClientOptions{})
	if err != nil {
		t.Fatal(err)
	}
	svc := NewProvisioningService(nil, nil, nil)
	svc.SetDockerClient(docker)
	return svc, &domain.DatabaseInstance{DBType: domain.PostgreSQL, DeploymentMode: domain.ModeDocker, Namespace: pg.container.GetContainerID()}
}

func TestRotationLeavesNothingInALogThatRecordsEveryStatement(t *testing.T) {
	logLeakImages(t, func(t *testing.T, start func(*testing.T) *rolePostgres) {
		pg := start(t)
		pg.psql(t, "CREATE ROLE rotated LOGIN PASSWORD 'old'")
		pg.psql(t, "ALTER SYSTEM SET log_statement = 'all'")
		pg.psql(t, "SELECT pg_reload_conf()")
		pg.waitForStatementLogging(t)
		svc, inst := rotationOn(t, pg)
		password := generatePassword(32)
		passwords := map[string]string{"rotated": password}
		if err := svc.alterRolePassword(context.Background(), inst, "rotated", password); err != nil {
			t.Fatalf("rotate: %v", err)
		}
		err := svc.alterRolePassword(context.Background(), inst, "no_such_role", password)
		if err == nil {
			t.Fatal("rotating a role that does not exist did not fail")
		}
		assertNoPassword(t, "returned error", err.Error(), passwords)
		assertNoVerifier(t, "returned error", err.Error())
		pg.waitForStatementLogging(t)
		log := pg.serverLog(t)
		if !strings.Contains(log, "no_such_role") {
			t.Fatal("the server log does not hold the failure, so this test proves nothing")
		}
		assertNoPassword(t, "server log", log, passwords)
		assertNoVerifier(t, "server log", log)
		var current string
		if err := pg.connect(t, "rotated", password).QueryRow("SELECT current_user").Scan(&current); err != nil {
			t.Errorf("the rotated role cannot sign in: %v", err)
		}
	})
}

// A rotation opens a plain session, so a tier's statement_timeout applies, and
// a customer on the role being rotated can hold its row until that fires.
// query_canceled is not among PL/pgSQL's OTHERS.
func TestRotationTimingOutNamesNothing(t *testing.T) {
	pg := startRolePostgresImage(t, "postgres:17")
	pg.psql(t, "CREATE ROLE rotated LOGIN PASSWORD 'Held-Password-1'")
	session := pg.connect(t, "rotated", "Held-Password-1")
	tx, err := session.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.Exec("ALTER ROLE rotated PASSWORD 'Held-Password-2'"); err != nil {
		t.Fatal(err)
	}
	pg.psql(t, "ALTER ROLE postgres SET statement_timeout = '500ms'")
	svc, inst := rotationOn(t, pg)
	err = svc.alterRolePassword(context.Background(), inst, "rotated", generatePassword(32))
	if err == nil || !strings.Contains(err.Error(), "statement timeout") {
		t.Fatalf("the rotation did not time out: %v", err)
	}
	assertNoVerifier(t, "returned error", err.Error())
	assertNoVerifier(t, "server log", pg.serverLog(t))
}

// Customer roles get a password the customer chose, so a verifier in the log is
// open to a dictionary. The functions run as the customer's session, which
// cannot silence the log, so the handlers have to drop the context themselves.
func TestCustomerRoleFunctionsTimingOutNameNothing(t *testing.T) {
	pg := startRolePostgresImage(t, "postgres:17")
	c := newPlatformSchemaCaseOn(t, pg)
	c.register(t)
	ctx := context.Background()
	conn, err := c.admin.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, "SET statement_timeout = '500ms'"); err != nil {
		t.Fatal(err)
	}
	verifier, err := newScramVerifier(generatePassword(24))
	if err != nil {
		t.Fatal(err)
	}

	t.Run("create_role", func(t *testing.T) {
		// A SHARE lock lets the function's guards read and stops only the CREATE.
		holder, err := c.admin.Begin()
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = holder.Rollback() }()
		if _, err := holder.Exec("LOCK TABLE pg_catalog.pg_authid IN SHARE MODE"); err != nil {
			t.Fatal(err)
		}
		_, err = conn.ExecContext(ctx, "SELECT excalibase.create_role('timed_out', $1, true)", verifier)
		if err == nil || !strings.Contains(err.Error(), "statement timeout") {
			t.Fatalf("create_role did not time out: %v", err == nil)
		}
		assertNoVerifier(t, "returned error", err.Error())
		assertNoVerifier(t, "server log", pg.serverLog(t))
	})

	t.Run("alter_role_password", func(t *testing.T) {
		held, err := newScramVerifier("Held-Password-1")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := conn.ExecContext(ctx, "SELECT excalibase.create_role('held_role', $1, true)", held); err != nil {
			t.Fatalf("create the role: %v", err)
		}
		tx, err := pg.connect(t, "held_role", "Held-Password-1").Begin()
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = tx.Rollback() }()
		if _, err := tx.Exec("ALTER ROLE held_role PASSWORD 'Held-Password-2'"); err != nil {
			t.Fatal(err)
		}
		_, err = conn.ExecContext(ctx, "SELECT excalibase.alter_role_password('held_role', $1)", verifier)
		if err == nil || !strings.Contains(err.Error(), "statement timeout") {
			t.Fatalf("alter_role_password did not time out: %v", err == nil)
		}
		assertNoVerifier(t, "returned error", err.Error())
		assertNoVerifier(t, "server log", pg.serverLog(t))
	})
}

// waitForBlockedScript finds the superuser backend waiting on a customer's lock.
func waitForBlockedScript(t *testing.T, admin *sql.DB) int {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		var pid int
		err := admin.QueryRow(`SELECT pid FROM pg_stat_activity WHERE usename = 'postgres' AND wait_event_type = 'Lock' LIMIT 1`).Scan(&pid)
		if err == nil {
			return pid
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("the role script never blocked")
	return 0
}

// blockedRoleScript runs the role SQL with the owner holding its own role row,
// so the script stops in the block that names the owner's verifier.
func blockedRoleScript(t *testing.T, major string) (*rolePostgres, *platformSchemaCase, *sql.Tx, <-chan error) {
	t.Helper()
	pg := startRolePostgresImage(t, major)
	c := newPlatformSchemaCaseOn(t, pg)
	c.registerAsBeforeEXC623(t)
	spec := c.spec
	spec.resetPasswords, spec.resetAdmin = true, true
	spec.adminPassword = generatePassword(32)
	tx, err := pg.connect(t, customerOwner, customerOwnerPassword).Begin()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tx.Rollback() })
	if _, err := tx.Exec("ALTER ROLE " + customerOwner + " PASSWORD 'Owner-Self-Held-1'"); err != nil {
		t.Fatal(err)
	}
	script := mustProjectRoleSQL(t, spec, logLeakCreds(), "cdc_watcher_pub")
	done := make(chan error, 1)
	go func() {
		done <- runSuperuserRoleSQL(context.Background(), pg.execer(), "app", reservedSchemaFirstSQL(), script)
	}()
	return pg, c, tx, done
}

// The customer's backend is the deadlock victim, under the customer's settings,
// and its DETAIL prints what the other process is running.
func TestDeadlockVictimDoesNotLogTheRoleScript(t *testing.T) {
	pg, c, tx, done := blockedRoleScript(t, "postgres:14")
	waitForBlockedScript(t, c.admin)
	time.Sleep(1300 * time.Millisecond) // past the script's own deadlock check
	_, err := tx.Exec("GRANT CONNECT ON DATABASE app TO PUBLIC")
	_ = tx.Rollback()
	if scriptErr := <-done; err == nil && scriptErr == nil {
		t.Fatal("no deadlock happened, so this test proves nothing")
	}
	log := pg.serverLog(t)
	if !strings.Contains(log, "deadlock detected") {
		t.Fatal("no deadlock in the server log, so this test proves nothing")
	}
	assertNoVerifier(t, "server log", log)
}

// A FATAL cannot be caught in PL/pgSQL, so the context reaches the client.
func TestFatalDuringTheRoleScriptReturnsNoVerifier(t *testing.T) {
	pg, c, _, done := blockedRoleScript(t, "postgres:14")
	pid := waitForBlockedScript(t, c.admin)
	if _, err := c.admin.Exec("SELECT pg_terminate_backend($1)", pid); err != nil {
		t.Fatal(err)
	}
	err := <-done
	if err == nil {
		t.Fatal("the terminated script reported success")
	}
	assertNoVerifier(t, "returned error", err.Error())
	assertNoVerifier(t, "server log", pg.serverLog(t))
}

// stdin that stops short leaves psql at EOF inside a transaction: it rolls back
// and exits 0, which must not read as success.
func TestRoleScriptCutShortIsAFailure(t *testing.T) {
	pg := startRolePostgresImage(t, "postgres:17")
	cutShort := func(ctx context.Context, cmd []string, stdin string) (string, error) {
		if stdin != "" {
			stdin = stdin[:strings.Index(stdin, "CREATE ROLE cut_short;")+len("CREATE ROLE cut_short;")] + "\n"
		}
		return pg.execer()(ctx, cmd, stdin)
	}
	if err := runSuperuserRoleSQL(context.Background(), cutShort, "app", "CREATE ROLE cut_short;"); err == nil {
		t.Fatal("a script that never reached its COMMIT reported success")
	}
}

// The engine stops reading stdin when psql exits, so a large script that fails
// early must still report the SQL error rather than a broken pipe.
func TestRealExecReportsTheSQLErrorOfALargeScript(t *testing.T) {
	pg := startRolePostgresImage(t, "postgres:17")
	padding := "-- " + strings.Repeat("x", 1000) + "\n"
	for _, size := range []int{1 << 20, 8 << 20} {
		_, err := pg.execer()(context.Background(), superuserPsqlCommand("app", 170000), "SELECT 1/0;\n"+strings.Repeat(padding, size/len(padding)))
		if err == nil || !strings.Contains(err.Error(), "division by zero") {
			t.Errorf("%d bytes after the failure: the SQL error was lost: %v", size, err)
		}
	}
}

// A verifier we compute has to open the door with whatever the password holds,
// for a client that normalizes it as Postgres does (libpq).
func TestPasswordsWithHostileBytesSignInThroughTheirVerifier(t *testing.T) {
	pg := startRolePostgresImage(t, "postgres:17")
	passwords := []string{"a$$; SELECT 1; --", `\'"`, "tab\tnewline\nend;", "pässwörd-ß", "ﬀ-composed-ℌ"}
	for i, password := range passwords {
		role := "hostile_" + string(rune('a'+i))
		pg.psql(t, "CREATE ROLE "+role+" LOGIN")
		pg.psql(t, BuildProjectRoleResetSQL(mustRoleVerifiers(t, []RolePassword{{Role: role, Password: password}})))
		// Not loopback: the image trusts that, and this must go through SCRAM.
		code, _, err := pg.container.Exec(context.Background(),
			[]string{"sh", "-c", `psql -h "$(hostname -i)" -U ` + role + ` -d app -tAc "SELECT 1"`},
			tcexec.WithEnv([]string{"PGPASSWORD=" + password}))
		if err != nil || code != 0 {
			t.Errorf("password %d cannot sign in through libpq: exit %d, %v", i, code, err)
		}
		if code, _, err := pg.container.Exec(context.Background(),
			[]string{"sh", "-c", `psql -h "$(hostname -i)" -U ` + role + ` -d app -tAc "SELECT 1"`},
			tcexec.WithEnv([]string{"PGPASSWORD=wrong"})); err != nil || code == 0 {
			t.Errorf("role %d signed in with the wrong password, so this login proves nothing", i)
		}
	}
}
