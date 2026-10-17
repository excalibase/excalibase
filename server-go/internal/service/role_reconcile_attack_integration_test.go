//go:build integration

package service

import (
	"context"
	"fmt"
	"io"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/testcontainers/testcontainers-go"
	tcexec "github.com/testcontainers/testcontainers-go/exec"
	"github.com/testcontainers/testcontainers-go/wait"
)

// A project registered before EXC-623 is brought up to date by re-running the
// registration SQL. Its owner holds the database, so it can attach settings to
// it or take a free role name to make that SQL fail; the schema must still be
// taken back from excalibase_app before anything the owner can break.

func TestRoleReconcileWithstandsTheOwner_Postgres15(t *testing.T) {
	roleReconcileAttackSuite(t, func(t *testing.T) *rolePostgres { return startRolePostgresImage(t, "postgres:15-alpine") }, "15")
}

func TestRoleReconcileWithstandsTheOwner_CNPGPostgres17(t *testing.T) {
	roleReconcileAttackSuite(t, func(t *testing.T) *rolePostgres { return startCNPGRolePostgres(t, "17") }, "17")
}

type ownerAttack struct {
	name string
	// statements run as the owner against a project registered before EXC-623.
	statements []string
	// blocksRegistration is true when the rest of the SQL may still refuse;
	// the schema lockdown must have landed either way.
	blocksRegistration bool
	minMajor           int
}

var ownerAttacks = []ownerAttack{
	{name: "no attack"},
	{name: "read-only default", statements: []string{"ALTER DATABASE app SET default_transaction_read_only = on"}},
	{name: "poisoned preload library", statements: []string{"ALTER DATABASE app SET local_preload_libraries = 'no_such_excalibase_lib'"}},
	{name: "role switch on connect", statements: []string{"ALTER DATABASE app SET role = '" + customerOwner + "'"}},
	{name: "timeouts", statements: []string{"ALTER DATABASE app SET statement_timeout = '1ms'", "ALTER DATABASE app SET lock_timeout = '1ms'"}},
	{name: "PL/pgSQL extra errors and exit on error", statements: []string{"DO $$ BEGIN END $$", "ALTER DATABASE app SET plpgsql.extra_errors = 'all'", "ALTER DATABASE app SET exit_on_error = on"}},
	// The command pins transaction_timeout only when the version probe finds a
	// server that has it; no stored major is consulted (finding 4).
	{name: "transaction timeout", statements: []string{"ALTER DATABASE app SET transaction_timeout = '1ms'"}, minMajor: 17},
	{name: "scheduler role squatted", statements: []string{"CREATE ROLE " + roleScheduler + " LOGIN PASSWORD 'Squat-1'"}, blocksRegistration: true, minMajor: 16},
}

func roleReconcileAttackSuite(t *testing.T, start func(*testing.T) *rolePostgres, major string) {
	majorNumber := 0
	fmt.Sscan(major, &majorNumber)
	for _, attack := range ownerAttacks {
		if majorNumber < attack.minMajor {
			continue
		}
		t.Run(attack.name, func(t *testing.T) {
			c := newPlatformSchemaCaseOn(t, start(t))
			c.registerAsBeforeEXC623(t)
			// One admin session opened before the attack, so the owner's
			// database settings do not reach the assertions.
			c.admin.SetMaxOpenConns(1)
			c.admin.SetMaxIdleConns(1)
			if err := c.admin.Ping(); err != nil {
				t.Fatal(err)
			}
			owner := c.pg.connect(t, customerOwner, customerOwnerPassword)
			for _, statement := range attack.statements {
				mustExec(t, owner, statement)
			}

			err := runSuperuserRoleSQL(context.Background(), c.pg.execer(), "app",
				reservedSchemaFirstSQL(), projectRoleSQL(c.spec, c.creds, "cdc_watcher_pub"))
			if attack.blocksRegistration {
				if err == nil {
					t.Fatal("registration went on over a squatted platform role")
				}
			} else if err != nil {
				t.Fatalf("the owner's setting stopped the registration SQL: %v", err)
			}
			c.assertSchemaTakenBack(t)
			if attack.blocksRegistration {
				// The squat stops the second script, but the CREATE EXTENSION
				// guard landed in the first one, so excalibase_app still cannot
				// place an extension in a reserved schema (finding 2).
				app := c.pg.connect(t, roleApp, customerAppPassword)
				if _, err := app.Exec("CREATE EXTENSION pgcrypto SCHEMA excalibase"); err == nil {
					t.Error("excalibase_app placed an extension in excalibase after a squat")
				}
				if _, err := app.Exec("CREATE EXTENSION hstore SCHEMA auth"); err == nil {
					t.Error("excalibase_app placed an extension in auth after a squat")
				}
			}
			if !attack.blocksRegistration {
				// The settings would reach the scheduler's own sessions too;
				// they are the owner's to undo, so the test undoes them.
				mustExec(t, c.admin, "ALTER DATABASE app RESET ALL")
				c.pg.assertLogin(t, roleScheduler, platformSchedulerPassword)
			}
		})
	}
}

// The registration SQL on its own, without the first transaction, also takes
// the schema back: a restore or a test that runs only it must not keep the
// app role's old grants.
func TestRegistrationSQLAloneTakesTheSchemaBack(t *testing.T) {
	c := newPlatformSchemaCase(t)
	c.registerAsBeforeEXC623(t)
	c.pg.psql(t, projectRoleSQL(c.spec, c.creds, "cdc_watcher_pub"))
	c.assertSchemaTakenBack(t)
}

// registerAsBeforeEXC623 leaves the grants a project registered before EXC-623
// had: excalibase_app may create in the schema and write its tables, and no
// scheduler login exists.
func (c *platformSchemaCase) registerAsBeforeEXC623(t *testing.T) {
	t.Helper()
	c.pg.psql(t, projectRoleSQL(c.spec, c.creds, "cdc_watcher_pub"))
	c.pg.psql(t, "DROP OWNED BY "+roleScheduler+"; DROP ROLE "+roleScheduler+";"+
		"GRANT CREATE ON SCHEMA excalibase TO "+roleApp+";"+
		"GRANT ALL ON ALL TABLES IN SCHEMA excalibase TO "+roleApp+";"+
		"GRANT ALL ON ALL SEQUENCES IN SCHEMA excalibase TO "+roleApp+";"+
		// The CREATE EXTENSION guard is new in EXC-623; an older project has
		// none, so the reconcile must install it.
		"DROP EVENT TRIGGER IF EXISTS excalibase_guard_reserved_schemas;"+
		"DROP ROUTINE IF EXISTS excalibase.guard_reserved_schemas()")
}

func (c *platformSchemaCase) assertSchemaTakenBack(t *testing.T) {
	t.Helper()
	if scanBool(t, c.admin, "SELECT has_schema_privilege($1, 'excalibase', 'CREATE')", roleApp) {
		t.Error("excalibase_app can still create in the excalibase schema")
	}
	for _, privilege := range []string{"INSERT", "UPDATE", "DELETE", "TRUNCATE", "TRIGGER"} {
		if scanBool(t, c.admin, "SELECT has_table_privilege($1, 'excalibase.excalibase_scheduled_functions', $2)", roleApp, privilege) {
			t.Errorf("excalibase_app still holds %s on the scheduler table", privilege)
		}
	}
	if !scanBool(t, c.admin, "SELECT r.rolsuper FROM pg_namespace n JOIN pg_roles r ON r.oid = n.nspowner WHERE n.nspname = 'excalibase'") {
		t.Error("the excalibase schema is not the superuser's")
	}
}

// execCommand runs argv in the database container, as provisioning does.
func (p *rolePostgres) execCommand(argv []string) error {
	code, out, err := p.container.Exec(context.Background(), argv, tcexec.Multiplexed())
	if err != nil {
		return err
	}
	body, _ := io.ReadAll(out)
	if code != 0 {
		return fmt.Errorf("psql exit %d: %s", code, body)
	}
	return nil
}

// execer drives role SQL through the container the way the service drives it
// through a pod or a docker container.
func (p *rolePostgres) execer() roleSQLExecer {
	return func(ctx context.Context, cmd []string) (string, error) {
		return p.execOutput(ctx, cmd)
	}
}

func (p *rolePostgres) execOutput(ctx context.Context, cmd []string) (string, error) {
	code, out, err := p.container.Exec(ctx, cmd, tcexec.Multiplexed())
	if err != nil {
		return "", err
	}
	body, _ := io.ReadAll(out)
	if code != 0 {
		return string(body), fmt.Errorf("psql exit %d: %s", code, body)
	}
	return string(body), nil
}

// A customer holding a lock on an excalibase object must not block the
// rebuild: with a finite lock_timeout the first attempt times out, the
// orchestration terminates the customer backend, and the retry lands. The
// platform roles are never terminated.
func TestRoleReconcileTerminatesALockHolder_Postgres15(t *testing.T) {
	roleReconcileLockHolderCase(t, startRolePostgresImage(t, "postgres:15-alpine"))
}

func TestRoleReconcileTerminatesALockHolder_CNPGPostgres17(t *testing.T) {
	roleReconcileLockHolderCase(t, startCNPGRolePostgres(t, "17"))
}

func roleReconcileLockHolderCase(t *testing.T, pg *rolePostgres) {
	c := newPlatformSchemaCaseOn(t, pg)
	c.registerAsBeforeEXC623(t)
	c.admin.SetMaxOpenConns(1)
	c.admin.SetMaxIdleConns(1)
	if err := c.admin.Ping(); err != nil {
		t.Fatal(err)
	}
	// Taint the schema so the rebuild must DROP it, which needs an exclusive
	// lock on a table a customer session is about to hold.
	app := c.pg.connect(t, roleApp, customerAppPassword)
	mustExec(t, app, "CREATE TABLE excalibase.app_planted (id int)")
	mustExec(t, c.admin, "GRANT USAGE ON SCHEMA excalibase TO "+customerOwner)
	mustExec(t, c.admin, "GRANT SELECT ON excalibase.app_planted TO "+customerOwner)

	owner := c.pg.connect(t, customerOwner, customerOwnerPassword)
	tx, err := owner.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.Exec("SELECT count(*) FROM excalibase.app_planted"); err != nil {
		t.Fatal(err)
	}

	if err := runSuperuserRoleSQL(context.Background(), c.pg.execer(), "app",
		reservedSchemaFirstSQL(), projectRoleSQL(c.spec, c.creds, "cdc_watcher_pub")); err != nil {
		t.Fatalf("a customer lock holder blocked the rebuild: %v", err)
	}
	c.assertSchemaTakenBack(t)
}

// startCNPGRolePostgres runs the CloudNativePG operand image a project of
// that major runs on. It has no entrypoint of its own, so the test initialises
// the cluster and creates the app database itself.
func startCNPGRolePostgres(t *testing.T, major string) *rolePostgres {
	t.Helper()
	image := ""
	for _, entry := range config.PostgresCatalogEntries() {
		if entry.Major == major {
			image = entry.BaseImage
		}
	}
	if image == "" {
		t.Fatalf("no catalogue entry for Postgres %s", major)
	}
	ctx := context.Background()
	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image:        image,
			ExposedPorts: []string{"5432/tcp"},
			Cmd: []string{"bash", "-c", "initdb -D /tmp/pgdata -U postgres --auth=trust >/dev/null && " +
				"echo 'host all all all trust' >> /tmp/pgdata/pg_hba.conf && " +
				"exec postgres -D /tmp/pgdata -c listen_addresses='*' -k /tmp"},
			Env:        map[string]string{"PGHOST": "/tmp"},
			WaitingFor: wait.ForLog("database system is ready to accept connections").WithStartupTimeout(90 * time.Second),
		},
		Started: true,
	})
	if err != nil {
		t.Fatalf("start CNPG postgres %s: %v", major, err)
	}
	t.Cleanup(func() { _ = container.Terminate(ctx) })
	host, err := container.Host(ctx)
	if err != nil {
		t.Fatalf("host: %v", err)
	}
	port, err := container.MappedPort(ctx, "5432/tcp")
	if err != nil {
		t.Fatalf("port: %v", err)
	}
	pg := &rolePostgres{container: container, host: host, port: port.Port()}
	if err := pg.execCommand([]string{"psql", "-U", "postgres", "-d", "postgres", "-c", "CREATE DATABASE app"}); err != nil {
		t.Fatalf("create app database: %v", err)
	}
	return pg
}
