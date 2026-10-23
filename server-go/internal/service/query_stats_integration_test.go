//go:build integration

package service

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/features"
)

// The settings CNPG renders for a cluster with the feature on (EXC-654).
var statsServerArgs = []string{"-c", "shared_preload_libraries=pg_stat_statements",
	"-c", "pg_stat_statements.track=top", "-c", "pg_stat_statements.track_utility=off"}

func startStatsPostgres(t *testing.T) *rolePostgres {
	t.Helper()
	ctx := context.Background()
	pg, err := postgres.Run(ctx, "postgres:16-alpine",
		postgres.WithDatabase("app"), postgres.WithUsername("postgres"), postgres.WithPassword("p"),
		testcontainers.WithCmd(append([]string{"postgres"}, statsServerArgs...)...),
		testcontainers.WithWaitStrategy(wait.ForLog("database system is ready to accept connections").
			WithOccurrence(2).WithStartupTimeout(45*time.Second)),
	)
	if err != nil {
		t.Fatalf("start postgres: %v", err)
	}
	t.Cleanup(func() { _ = pg.Terminate(ctx) })
	host, _ := pg.Host(ctx)
	port, _ := pg.MappedPort(ctx, "5432/tcp")
	return &rolePostgres{container: pg, host: host, port: port.Port()}
}

// startStatsCNPGPostgres runs the oldest catalogued CloudNativePG image the
// way the operator would with the feature on.
func startStatsCNPGPostgres(t *testing.T) *rolePostgres {
	t.Helper()
	entry := config.PostgresCatalogEntries()[0]
	ctx := context.Background()
	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image:        entry.BaseImage,
			ExposedPorts: []string{"5432/tcp"},
			Cmd: []string{"bash", "-c", "initdb -D /tmp/pgdata -U postgres --auth=trust >/dev/null && " +
				"echo 'host all all all trust' >> /tmp/pgdata/pg_hba.conf && " +
				"exec postgres -D /tmp/pgdata -c listen_addresses='*' -k /tmp " + strings.Join(statsServerArgs, " ")},
			Env:        map[string]string{"PGHOST": "/tmp"},
			WaitingFor: wait.ForLog("database system is ready to accept connections").WithStartupTimeout(90 * time.Second),
		},
		Started: true,
	})
	if err != nil {
		t.Fatalf("start CNPG postgres %s: %v", entry.Major, err)
	}
	t.Cleanup(func() { _ = container.Terminate(ctx) })
	host, _ := container.Host(ctx)
	port, _ := container.MappedPort(ctx, "5432/tcp")
	pg := &rolePostgres{container: container, host: host, port: port.Port()}
	if err := pg.execCommand([]string{"psql", "-U", "postgres", "-d", "postgres", "-c", "CREATE DATABASE app"}); err != nil {
		t.Fatalf("create app database: %v", err)
	}
	return pg
}

// containerDatabases drives the service's psql through the test container.
type containerDatabases struct{ pg *rolePostgres }

func (c containerDatabases) primaryPsql(context.Context, *domain.DatabaseInstance) (roleSQLExecer, error) {
	return c.pg.execer(), nil
}

func (containerDatabases) EnableQueryStats(context.Context, string) error { return nil }

// statsWorkload gives every kind of role statements in the project database
// and another database, as CNPG leaves the extension in each.
func statsWorkload(t *testing.T, pg *rolePostgres) {
	t.Helper()
	pg.psql(t, `CREATE EXTENSION IF NOT EXISTS pg_stat_statements;
CREATE ROLE excalibase_app LOGIN PASSWORD 'app-pw';
CREATE ROLE shop_owner LOGIN PASSWORD 'owner-pw';
CREATE ROLE auth_admin LOGIN PASSWORD 'auth-pw';
CREATE ROLE excalibase_scheduler LOGIN PASSWORD 'sched-pw';
CREATE ROLE cdc_watcher LOGIN REPLICATION PASSWORD 'cdc-pw';
CREATE TABLE public.orders (id int PRIMARY KEY, secret_note text);
GRANT ALL ON public.orders TO excalibase_app, shop_owner, auth_admin, excalibase_scheduler, cdc_watcher;`)
	pg.psql(t, "CREATE DATABASE other")
	run := func(user, password, database, statement string) {
		db := pg.connectTo(t, user, password, database)
		if _, err := db.Exec(statement); err != nil {
			t.Fatalf("%s: %s: %v", user, statement, err)
		}
	}
	run("shop_owner", "owner-pw", "app", "INSERT INTO orders VALUES (1, 'owner-literal-4242')")
	run("excalibase_app", "app-pw", "app", "SELECT * FROM orders WHERE secret_note = 'api-literal-9191'")
	run("auth_admin", "auth-pw", "app", "SELECT count(*) AS auth_marker FROM orders")
	run("excalibase_scheduler", "sched-pw", "app", "SELECT count(*) AS scheduler_marker FROM orders")
	run("cdc_watcher", "cdc-pw", "app", "SELECT count(*) AS cdc_marker FROM orders")
	run("postgres", "p", "app", "SELECT count(*) AS superuser_marker FROM orders")
	run("shop_owner", "owner-pw", "other", "SELECT 'other-database-marker' AS other_marker")
}

func (p *rolePostgres) connectTo(t *testing.T, user, password, database string) *sql.DB {
	t.Helper()
	db, err := sql.Open("postgres", "host="+p.host+" port="+p.port+" user="+user+" password="+password+" dbname="+database+" sslmode=disable")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func statsService(pg *rolePostgres) *QueryStatsService {
	inst := &domain.DatabaseInstance{ProjectID: "p1", Namespace: "org-p1", DBType: domain.PostgreSQL, Status: "ACTIVE", DatabaseName: "app"}
	return NewQueryStatsService(statsProjects(inst), containerDatabases{pg: pg}, nil, features.NewStatic())
}

func TestIntegration_SlowQueriesArePostgresProjectRolesOnly(t *testing.T) {
	for name, start := range map[string]func(*testing.T) *rolePostgres{
		"postgres 16":            startStatsPostgres,
		"oldest catalogued CNPG": startStatsCNPGPostgres,
	} {
		t.Run(name, func(t *testing.T) {
			pg := start(t)
			statsWorkload(t, pg)
			svc := statsService(pg)
			report, err := svc.List(context.Background(), "p1", "total", 100)
			if err != nil {
				t.Fatal(err)
			}
			if !report.Enabled {
				t.Fatal("statistics reported off with the library preloaded")
			}
			text := ""
			for _, q := range report.Queries {
				text += q.Query + "\n"
			}
			for _, want := range []string{"INSERT INTO orders VALUES ($1, $2)", "SELECT * FROM orders WHERE secret_note = $1"} {
				if !strings.Contains(text, want) {
					t.Errorf("missing the project's %q in:\n%s", want, text)
				}
			}
			for _, refused := range []string{"auth_marker", "scheduler_marker", "cdc_marker", "superuser_marker", "other_marker",
				"owner-literal-4242", "api-literal-9191", "pg_temp", "pg_stat_statements"} {
				if strings.Contains(text, refused) {
					t.Errorf("answered %q:\n%s", refused, text)
				}
			}

			if err := svc.Reset(context.Background(), "p1"); err != nil {
				t.Fatalf("reset: %v", err)
			}
			after, err := svc.List(context.Background(), "p1", "mean", 100)
			if err != nil {
				t.Fatal(err)
			}
			if len(after.Queries) != 0 {
				t.Errorf("reset left %d of the project's statements", len(after.Queries))
			}
			// The platform's own statistics are not the project's to clear.
			admin := pg.connectTo(t, "postgres", "p", "app")
			var kept int
			if err := admin.QueryRow(`SELECT count(*) FROM pg_stat_statements s JOIN pg_roles r ON r.oid = s.userid
				WHERE r.rolname = 'auth_admin' OR s.query LIKE '%other_marker%'`).Scan(&kept); err != nil {
				t.Fatal(err)
			}
			if kept < 2 {
				t.Errorf("reset cleared the platform's or another database's statistics: %d left", kept)
			}

			pg.psql(t, "DROP EXTENSION pg_stat_statements")
			off, err := svc.List(context.Background(), "p1", "total", 10)
			if err != nil || off.Enabled {
				t.Errorf("without the extension: %+v, %v", off, err)
			}
			if err := svc.Reset(context.Background(), "p1"); err == nil {
				t.Error("reset without the extension reported success")
			}
		})
	}
}

// An owner's database settings cannot turn the read against the platform.
func TestIntegration_SlowQueriesWithstandTheOwnersDatabaseSettings(t *testing.T) {
	pg := startStatsPostgres(t)
	statsWorkload(t, pg)
	pg.psql(t, "ALTER DATABASE app SET role = 'shop_owner'; ALTER DATABASE app SET search_path = 'public'; ALTER DATABASE app SET statement_timeout = '1ms'")
	report, err := statsService(pg).List(context.Background(), "p1", "total", 100)
	if err != nil {
		t.Fatal(err)
	}
	if !report.Enabled || len(report.Queries) == 0 {
		t.Fatalf("report = %+v", report)
	}
}
