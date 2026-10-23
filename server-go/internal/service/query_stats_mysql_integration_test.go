//go:build integration

package service

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/go-sql-driver/mysql"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/features"
	"github.com/excalibase/provisioning-poc/internal/provisioner"
)

type statsMySQL struct{ addr string }

func startStatsMySQL(t *testing.T) statsMySQL {
	t.Helper()
	ctx := context.Background()
	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image:        "mysql:8.4",
			Env:          map[string]string{"MYSQL_ROOT_PASSWORD": "root-pw", "MYSQL_DATABASE": "shop"},
			ExposedPorts: []string{"3306/tcp"},
			WaitingFor:   wait.ForLog("port: 3306  MySQL Community Server").WithStartupTimeout(3 * time.Minute),
		},
		Started: true,
	})
	if err != nil {
		t.Fatalf("start mysql: %v", err)
	}
	t.Cleanup(func() { _ = container.Terminate(context.Background()) })
	port, _ := container.MappedPort(ctx, "3306/tcp")
	return statsMySQL{addr: "localhost:" + port.Port()}
}

// connect logs in over the server's own certificate, as the platform users require TLS.
func (m statsMySQL) connect(t *testing.T, user, password, database string, multi bool) *sql.DB {
	t.Helper()
	config := mysql.NewConfig()
	config.Net, config.Addr, config.User, config.Passwd, config.DBName = "tcp", m.addr, user, password, database
	config.TLSConfig = "skip-verify"
	config.MultiStatements = multi
	connector, err := mysql.NewConnector(config)
	if err != nil {
		t.Fatal(err)
	}
	db := sql.OpenDB(connector)
	t.Cleanup(func() { _ = db.Close() })
	return db
}

type statsOpener struct{ db *sql.DB }

func (o statsOpener) OpenMySQLStats(context.Context, string) (*sql.DB, error) { return o.db, nil }

func mustExecMySQL(t *testing.T, db *sql.DB, statement string) {
	t.Helper()
	if _, err := db.Exec(statement); err != nil {
		t.Fatalf("%s: %v", statement, err)
	}
}

// EXC-654: on MySQL the digests of the project database are read and cleared
// as excalibase_app, which alone is granted them; the auth and platform
// databases' statements never come back and the customer's admin cannot read
// the table at all.
func TestIntegration_SlowQueriesOnMySQLAreTheProjectDatabasesOnly(t *testing.T) {
	server := startStatsMySQL(t)
	root := server.connect(t, "root", "root-pw", "", true)
	mustExecMySQL(t, root, provisioner.MySQLPlatformUsersSQL("shop", provisioner.MySQLPasswords{
		Admin: "admin-pw", App: "app-pw", AuthAdmin: "auth-pw", Watcher: "cdc-pw"})+
		provisioner.MySQLQueryStatsGrantSQL+
		"CREATE TABLE shop.orders (id int PRIMARY KEY, note text); CREATE TABLE auth.users (id int PRIMARY KEY);")

	admin := server.connect(t, "admin", "admin-pw", "shop", false)
	mustExecMySQL(t, admin, "INSERT INTO orders VALUES (1, 'admin-literal-777')")
	mustExecMySQL(t, server.connect(t, "auth_admin", "auth-pw", "auth", false), "SELECT count(*) AS auth_marker FROM users")
	if _, err := admin.Exec("SELECT * FROM performance_schema.events_statements_summary_by_digest"); err == nil {
		t.Error("the customer's admin read every database's digests")
	}

	app := server.connect(t, "excalibase_app", "app-pw", "shop", false)
	if _, err := app.Exec("SELECT QUERY_SAMPLE_TEXT FROM performance_schema.events_statements_summary_by_digest"); err == nil {
		t.Error("excalibase_app read the sample text, which keeps literals")
	}
	inst := &domain.DatabaseInstance{ProjectID: "m1", DBType: domain.MySQL, Status: "ACTIVE", DatabaseName: "shop"}
	svc := NewQueryStatsService(statsProjects(inst), &fakeStatsDatabases{}, statsOpener{db: app}, features.NewStatic(features.MySQL))
	ctx := context.Background()
	report, err := svc.List(ctx, "m1", "total", 100)
	if err != nil {
		t.Fatal(err)
	}
	if !report.Enabled {
		t.Fatal("statistics reported off with the grant in place")
	}
	text := ""
	for _, q := range report.Queries {
		text += q.Query + "\n"
		if q.Calls < 1 || q.TotalTimeMs <= 0 {
			t.Errorf("no cost on %+v", q)
		}
	}
	if !strings.Contains(text, "INSERT INTO `orders` VALUES (...)") {
		t.Errorf("the project's insert is missing:\n%s", text)
	}
	for _, refused := range []string{"auth_marker", "admin-literal-777", "performance_schema"} {
		if strings.Contains(text, refused) {
			t.Errorf("answered %q:\n%s", refused, text)
		}
	}

	if err := svc.Reset(ctx, "m1"); err != nil {
		t.Fatalf("reset: %v", err)
	}
	after, err := svc.List(ctx, "m1", "mean", 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(after.Queries) != 0 {
		t.Errorf("reset left %+v", after.Queries)
	}

	// A login without the grant, as excalibase_app is on a project created before the feature.
	mustExecMySQL(t, root, "CREATE USER 'ungranted'@'%' IDENTIFIED BY 'ungranted-pw' REQUIRE SSL; GRANT ALL ON shop.* TO 'ungranted'@'%'")
	svc = NewQueryStatsService(statsProjects(inst), &fakeStatsDatabases{}, statsOpener{db: server.connect(t, "ungranted", "ungranted-pw", "shop", false)},
		features.NewStatic(features.MySQL))
	off, err := svc.List(ctx, "m1", "total", 10)
	if err != nil || off.Enabled {
		t.Errorf("without the grant: %+v, %v", off, err)
	}
	if err := svc.Reset(ctx, "m1"); err == nil {
		t.Error("reset without the grant reported success")
	}
}
