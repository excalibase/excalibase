//go:build integration

package service

import (
	"context"
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/features"
	"github.com/excalibase/provisioning-poc/internal/provisioner"
)

// EXC-673: on a single host the extension lives only in the maintenance
// database; the read and reset run there and answer the project database's
// project-role statements only, whatever the owner did to its own database.

func singleHostStatsWorkload(t *testing.T, pg *rolePostgres) {
	t.Helper()
	pg.psqlIn(t, "postgres", `CREATE SCHEMA `+provisioner.QueryStatsSchema+`;
REVOKE ALL ON SCHEMA `+provisioner.QueryStatsSchema+` FROM PUBLIC;
CREATE EXTENSION pg_stat_statements SCHEMA `+provisioner.QueryStatsSchema+`;`)
	pg.psql(t, `CREATE ROLE excalibase_app LOGIN PASSWORD 'app-pw';
CREATE ROLE shop_owner LOGIN PASSWORD 'owner-pw';
CREATE ROLE excalibase_scheduler LOGIN PASSWORD 'sched-pw';
ALTER DATABASE app OWNER TO shop_owner;
CREATE TABLE public.orders (id int PRIMARY KEY, secret_note text);
GRANT ALL ON public.orders TO excalibase_app, shop_owner, excalibase_scheduler;`)
	pg.psql(t, "CREATE DATABASE other")
	run := func(user, password, database, statement string) {
		db := pg.connectTo(t, user, password, database)
		if _, err := db.Exec(statement); err != nil {
			t.Fatalf("%s: %s: %v", user, statement, err)
		}
	}
	run("shop_owner", "owner-pw", "app", "INSERT INTO orders VALUES (1, 'owner-literal-4242')")
	run("excalibase_app", "app-pw", "app", "SELECT * FROM orders WHERE secret_note = 'api-literal-9191'")
	run("excalibase_scheduler", "sched-pw", "app", "SELECT count(*) AS scheduler_marker FROM orders")
	run("postgres", "p", "app", "SELECT count(*) AS superuser_marker FROM orders")
	run("shop_owner", "owner-pw", "other", "SELECT 'other-database-marker' AS other_marker")
	// The owner poisons what it can: its database's settings and catalog look-alikes in it.
	run("shop_owner", "owner-pw", "app", `CREATE FUNCTION public.lower(text) RETURNS text LANGUAGE sql AS 'SELECT ''owned''::text'`)
	run("shop_owner", "owner-pw", "app", "ALTER DATABASE app SET search_path = public, pg_catalog")
	run("shop_owner", "owner-pw", "app", "ALTER DATABASE app SET statement_timeout = '1ms'")
}

func (p *rolePostgres) psqlIn(t *testing.T, database, sqlText string) {
	t.Helper()
	if err := p.execCommand([]string{"psql", "-U", "postgres", "-d", database, "-v", "ON_ERROR_STOP=1", "-c", sqlText}); err != nil {
		t.Fatalf("psql in %s: %v", database, err)
	}
}

func singleHostStatsService(pg *rolePostgres) *QueryStatsService {
	inst := &domain.DatabaseInstance{ProjectID: "p1", Namespace: "excalibase-p1-postgres", DBType: domain.PostgreSQL,
		Status: "ACTIVE", DatabaseName: "app", DeploymentMode: domain.ModeDocker}
	return NewQueryStatsService(statsProjects(inst), containerDatabases{pg: pg}, nil, features.NewStatic())
}

func TestIntegration_SingleHostSlowQueriesReadTheMaintenanceDatabase(t *testing.T) {
	pg := startStatsPostgres(t)
	singleHostStatsWorkload(t, pg)
	svc := singleHostStatsService(pg)

	report, err := svc.List(context.Background(), "p1", "total", 100)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if !report.Enabled {
		t.Fatal("statistics reported off")
	}
	var texts []string
	for _, query := range report.Queries {
		texts = append(texts, query.Query)
	}
	all := strings.Join(texts, "\n")
	for _, want := range []string{"INSERT INTO orders", "secret_note = $1"} {
		if !strings.Contains(all, want) {
			t.Errorf("missing the project's %q in:\n%s", want, all)
		}
	}
	for _, refused := range []string{"scheduler_marker", "superuser_marker", "other_marker", "owner-literal-4242", "api-literal-9191"} {
		if strings.Contains(all, refused) {
			t.Errorf("answered %q:\n%s", refused, all)
		}
	}

	if err := svc.Reset(context.Background(), "p1"); err != nil {
		t.Fatalf("Reset: %v", err)
	}
	after, err := svc.List(context.Background(), "p1", "total", 100)
	if err != nil || len(after.Queries) != 0 {
		t.Fatalf("after reset: %+v, %v", after, err)
	}
	// The reset cleared the project's rows only: the platform's statements in its database remain.
	var kept int
	db := pg.connectTo(t, "postgres", "p", "postgres")
	if err := db.QueryRow(`SELECT count(*) FROM ` + provisioner.QueryStatsSchema + `.pg_stat_statements s
JOIN pg_database d ON d.oid = s.dbid WHERE d.datname = 'app' AND s.query LIKE '%superuser_marker%'`).Scan(&kept); err != nil || kept != 1 {
		t.Errorf("superuser rows after reset = %d, %v", kept, err)
	}
}

// Without the extension in the maintenance database the page says off.
func TestIntegration_SingleHostSlowQueriesOffWithoutTheExtension(t *testing.T) {
	pg := startStatsPostgres(t)
	report, err := singleHostStatsService(pg).List(context.Background(), "p1", "total", 10)
	if err != nil || report.Enabled {
		t.Fatalf("report = %+v, err = %v", report, err)
	}
}
