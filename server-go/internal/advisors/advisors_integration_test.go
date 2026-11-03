//go:build integration

package advisors

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/go-sql-driver/mysql"
	_ "github.com/lib/pq"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/excalibase/provisioning-poc/internal/domain"
)

// EXC-655: each check against a real catalog, and a clean schema that reports nothing.

const problemsPostgres = `
CREATE SCHEMA shop;
CREATE TABLE shop.customers (id bigserial PRIMARY KEY, name text);
CREATE TABLE shop.orders (id bigserial PRIMARY KEY, customer_id bigint REFERENCES shop.customers (id));
CREATE TABLE shop.lines (id bigserial PRIMARY KEY, order_id bigint REFERENCES shop.orders (id));
CREATE INDEX lines_order ON shop.lines (order_id);
CREATE TABLE shop.items (id bigserial PRIMARY KEY, sku text);
CREATE INDEX items_sku_a ON shop.items (sku);
CREATE INDEX items_sku_b ON shop.items (sku);
CREATE TABLE shop.logs (message text);
CREATE TABLE shop.notes (id bigserial PRIMARY KEY, body text);
CREATE INDEX notes_body ON shop.notes (body);
INSERT INTO shop.notes (body) SELECT 'n' || g FROM generate_series(1, 50) g;
CREATE TABLE shop.big (id int PRIMARY KEY, v int);
INSERT INTO shop.big SELECT g, g % 100 FROM generate_series(1, 2000) g;
INSERT INTO shop.lines (order_id) VALUES (NULL);
ANALYZE;
-- Platform schemas carry the same problems and are never reported.
CREATE SCHEMA excalibase;
CREATE TABLE excalibase.jobs (x int);
CREATE SCHEMA auth;
CREATE TABLE auth.users (id bigserial PRIMARY KEY, org_id bigint REFERENCES auth.users (id));`

// readsPostgres reads tables the way an app would, then flushes the counters.
const readsPostgres = `
SET enable_indexscan = off; SET enable_bitmapscan = off;
SELECT count(*) FROM shop.notes WHERE body = 'n1';
SELECT count(*) FROM shop.notes WHERE body = 'n2';
SELECT count(*) FROM shop.notes WHERE body = 'n3';
SELECT count(*) FROM shop.notes WHERE body = 'n4';
SELECT count(*) FROM shop.notes WHERE body = 'n5';
SELECT count(*) FROM shop.lines;
SELECT count(*) FROM shop.big WHERE v = 5;
SELECT count(*) FROM shop.big WHERE v = 6;
SELECT count(*) FROM shop.big WHERE v = 7;
SELECT pg_stat_force_next_flush();`

const cleanPostgres = `
CREATE TABLE customers (id bigserial PRIMARY KEY, name text);
CREATE TABLE orders (id bigserial PRIMARY KEY, customer_id bigint REFERENCES customers (id));
CREATE INDEX orders_customer ON orders (customer_id);
INSERT INTO customers (name) VALUES ('a');
INSERT INTO orders (customer_id) VALUES (1);
CREATE TABLE events (id int PRIMARY KEY, v int);
CREATE INDEX events_v ON events (v);
INSERT INTO events SELECT g, g % 100 FROM generate_series(1, 2000) g;
ANALYZE;
CREATE SCHEMA excalibase;
CREATE TABLE excalibase.jobs (x int);`

const readsCleanPostgres = `
SET enable_seqscan = off;
SELECT count(*) FROM orders WHERE customer_id = 1;
SELECT count(*) FROM customers WHERE id = 1;
SELECT count(*) FROM events WHERE v = 5;
SELECT pg_stat_force_next_flush();`

var openToAnon = &domain.PermissionDocument{Tables: []domain.TablePermissions{
	{Table: "shop.orders", Role: "anon", Select: raw(`{"filter":{},"columns":"*"}`)},
	{Table: "shop.items", Role: "editor", Update: raw(`{"filter":{}}`)},
}}

type found struct{ check, object, index string }

func keysOf(findings []Finding) []found {
	out := make([]found, 0, len(findings))
	for _, f := range findings {
		out = append(out, found{f.Check, f.Object, f.Index})
	}
	sort.Slice(out, func(i, j int) bool { return fmt.Sprint(out[i]) < fmt.Sprint(out[j]) })
	return out
}

func expectExactly(t *testing.T, report *Report, want []found) {
	t.Helper()
	sort.Slice(want, func(i, j int) bool { return fmt.Sprint(want[i]) < fmt.Sprint(want[j]) })
	if got := keysOf(report.Findings); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("findings:\n got %v\nwant %v\nfull: %+v", got, want, report.Findings)
	}
}

// runUntil reruns the advisors until the statistics show the reads.
func runUntil(t *testing.T, svc *Service, ready func(*Report) bool) *Report {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for {
		report, err := svc.Run(context.Background(), "p1")
		if err != nil {
			t.Fatalf("run: %v", err)
		}
		if ready(report) || time.Now().After(deadline) {
			return report
		}
		time.Sleep(500 * time.Millisecond)
	}
}

func hasCheck(check string) func(*Report) bool {
	return func(r *Report) bool { return len(onlyCheck(r.Findings, check)) > 0 }
}

func TestIntegration_PostgresAdvisors(t *testing.T) {
	ctx := context.Background()
	admin := startPostgres(t)
	for _, statement := range []string{"CREATE DATABASE problems", "CREATE DATABASE clean"} {
		if _, err := admin.db.ExecContext(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}

	t.Run("problems", func(t *testing.T) {
		db := admin.open(t, "problems")
		seed(t, db, problemsPostgres, readsPostgres)
		svc := NewService(fakePools{postgres: db}, fakePermissions{doc: openToAnon}, Options{LargeTableRows: 1000})
		report := runUntil(t, svc, hasCheck(CheckLargeTableNoIndexUse))
		if report.Engine != EnginePostgres {
			t.Fatalf("engine = %s", report.Engine)
		}
		expectExactly(t, report, []found{
			{CheckFKWithoutIndex, "shop.orders", ""},
			{CheckDuplicateIndex, "shop.items", "items_sku_b"},
			{CheckNoPrimaryKey, "shop.logs", ""},
			{CheckUnusedIndex, "shop.notes", "notes_body"},
			{CheckLargeTableNoIndexUse, "shop.big", ""},
			{CheckAnonUnfiltered, "shop.orders", ""},
			{CheckWriteWithoutSelect, "shop.items", ""},
		})
		for _, status := range report.Checks {
			if status.Status != StatusRan {
				t.Errorf("check %+v did not run", status)
			}
		}
		// Every suggested statement is valid SQL against this schema.
		for _, f := range report.Findings {
			if f.FixSQL == "" {
				continue
			}
			if _, err := db.ExecContext(ctx, f.FixSQL); err != nil {
				t.Errorf("%s: %s: %v", f.Check, f.FixSQL, err)
			}
		}
	})

	t.Run("clean schema", func(t *testing.T) {
		db := admin.open(t, "clean")
		seed(t, db, cleanPostgres, readsCleanPostgres)
		svc := NewService(fakePools{postgres: db}, fakePermissions{doc: &domain.PermissionDocument{}}, Options{LargeTableRows: 1000})
		time.Sleep(2 * time.Second)
		expectExactly(t, runUntil(t, svc, func(*Report) bool { return true }), []found{})
	})
}

func TestIntegration_MySQLAdvisors(t *testing.T) {
	ctx := context.Background()
	root := startMySQL(t)
	seed(t, root.open(t, "app"), problemsMySQL, readsMySQL)

	t.Run("problems", func(t *testing.T) {
		svc := NewService(fakePools{mysql: root.open(t, "app")}, fakePermissions{doc: openToAnonMySQL}, Options{LargeTableRows: 1000})
		report := runUntil(t, svc, hasCheck(CheckLargeTableNoIndexUse))
		if report.Engine != EngineMySQL {
			t.Fatalf("engine = %s", report.Engine)
		}
		expectExactly(t, report, []found{
			{CheckDuplicateIndex, "app.items", "items_sku_b"},
			{CheckNoPrimaryKey, "app.logs", ""},
			{CheckUnusedIndex, "app.notes", "notes_body"},
			{CheckLargeTableNoIndexUse, "app.big", ""},
			{CheckAnonUnfiltered, "app.orders", ""},
		})
		if status := statusOf(report, CheckFKWithoutIndex); status.Status != StatusNotApplicable {
			t.Errorf("fk: %+v", status)
		}
		db := root.open(t, "app")
		for _, f := range report.Findings {
			if f.FixSQL == "" {
				continue
			}
			if _, err := db.ExecContext(ctx, f.FixSQL); err != nil {
				t.Errorf("%s: %s: %v", f.Check, f.FixSQL, err)
			}
		}
	})

	// The platform's admin login holds the project database only: index use
	// is reported unavailable, never as clean.
	t.Run("admin login", func(t *testing.T) {
		svc := NewService(fakePools{mysql: root.openAs(t, "app", "admin", "admin-pw")}, fakePermissions{doc: &domain.PermissionDocument{}}, Options{LargeTableRows: 1000})
		report, err := svc.Run(ctx, "p1")
		if err != nil {
			t.Fatalf("run: %v", err)
		}
		for _, check := range []string{CheckUnusedIndex, CheckLargeTableNoIndexUse} {
			if status := statusOf(report, check); status.Status != StatusUnavailable {
				t.Errorf("%s: %+v", check, status)
			}
		}
		if len(onlyCheck(report.Findings, CheckNoPrimaryKey)) != 1 {
			t.Errorf("catalog checks did not run: %+v", report.Findings)
		}
	})

	t.Run("clean schema", func(t *testing.T) {
		svc := NewService(fakePools{mysql: root.open(t, "clean")}, fakePermissions{doc: &domain.PermissionDocument{}}, Options{LargeTableRows: 1})
		expectExactly(t, runUntil(t, svc, func(*Report) bool { return true }), []found{})
	})
}

var openToAnonMySQL = &domain.PermissionDocument{Tables: []domain.TablePermissions{
	{Table: "app.orders", Role: "anon", Select: raw(`{"filter":{},"columns":"*"}`)},
	{Table: "app.items", Role: "user", Select: raw(`{"filter":{"id":{"_eq":1}}}`), Insert: raw(`{"check":{}}`)},
}}

const problemsMySQL = `
CREATE TABLE customers (id bigint unsigned auto_increment PRIMARY KEY, name varchar(50));
CREATE TABLE orders (id bigint unsigned auto_increment PRIMARY KEY, customer_id bigint unsigned,
  CONSTRAINT orders_customer FOREIGN KEY (customer_id) REFERENCES customers (id));
CREATE TABLE items (id bigint unsigned auto_increment PRIMARY KEY, sku varchar(40));
CREATE INDEX items_sku_a ON items (sku);
CREATE INDEX items_sku_b ON items (sku);
CREATE TABLE logs (message varchar(200));
CREATE TABLE notes (id bigint unsigned auto_increment PRIMARY KEY, body varchar(100));
CREATE INDEX notes_body ON notes (body);
INSERT INTO notes (body) VALUES ('a'), ('b'), ('c');
CREATE TABLE big (id int PRIMARY KEY, v int);
SET SESSION cte_max_recursion_depth = 5000;
INSERT INTO big SELECT n, n % 100 FROM (WITH RECURSIVE g (n) AS (SELECT 1 UNION ALL SELECT n + 1 FROM g WHERE n < 2000) SELECT n FROM g) AS numbers;
ANALYZE TABLE big;
CREATE DATABASE clean;
CREATE TABLE clean.customers (id bigint unsigned auto_increment PRIMARY KEY, name varchar(50));
CREATE TABLE clean.orders (id bigint unsigned auto_increment PRIMARY KEY, customer_id bigint unsigned,
  CONSTRAINT clean_orders_customer FOREIGN KEY (customer_id) REFERENCES clean.customers (id));
CREATE DATABASE auth;
CREATE TABLE auth.sessions (token varchar(64));
CREATE USER 'admin'@'%' IDENTIFIED BY 'admin-pw';
GRANT ALL PRIVILEGES ON app.* TO 'admin'@'%';`

const readsMySQL = `
SELECT COUNT(*) FROM notes IGNORE INDEX (notes_body) WHERE body LIKE '%b%';
SELECT COUNT(*) FROM big WHERE v = 5;
SELECT COUNT(*) FROM big WHERE v = 6;
SELECT COUNT(*) FROM big WHERE v = 7;`

func seed(t *testing.T, db *sql.DB, statements ...string) {
	t.Helper()
	conn, err := db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	for _, script := range statements {
		for _, statement := range strings.Split(script, ";\n") {
			if strings.TrimSpace(statement) == "" {
				continue
			}
			rows, err := conn.QueryContext(context.Background(), statement)
			if err != nil {
				t.Fatalf("seed %q: %v", statement, err)
			}
			_ = rows.Close()
		}
	}
}

type server struct {
	db   *sql.DB
	open func(t *testing.T, database string) *sql.DB
}

func startPostgres(t *testing.T) server {
	t.Helper()
	ctx := context.Background()
	container, err := postgres.Run(ctx, "postgres:16-alpine",
		postgres.WithDatabase("app"), postgres.WithUsername("app"), postgres.WithPassword("app-pw"),
		testcontainers.WithWaitStrategy(wait.ForLog("database system is ready to accept connections").WithOccurrence(2).WithStartupTimeout(time.Minute)))
	if err != nil {
		t.Fatalf("start postgres: %v", err)
	}
	t.Cleanup(func() { _ = container.Terminate(context.Background()) })
	host, _ := container.Host(ctx)
	port, _ := container.MappedPort(ctx, "5432/tcp")
	open := func(t *testing.T, database string) *sql.DB {
		dsn := fmt.Sprintf("host=%s port=%s user=app password=app-pw dbname=%s sslmode=disable", host, port.Port(), database)
		db, err := sql.Open("postgres", dsn)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = db.Close() })
		return db
	}
	return server{db: open(t, "app"), open: open}
}

type mysqlServer struct {
	addr string
}

func startMySQL(t *testing.T) mysqlServer {
	t.Helper()
	ctx := context.Background()
	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image:        "mysql:8.4",
			Env:          map[string]string{"MYSQL_ROOT_PASSWORD": "root-pw", "MYSQL_DATABASE": "app"},
			ExposedPorts: []string{"3306/tcp"},
			WaitingFor:   wait.ForLog("port: 3306  MySQL Community Server").WithStartupTimeout(3 * time.Minute),
		},
		Started: true,
	})
	if err != nil {
		t.Fatalf("start mysql: %v", err)
	}
	t.Cleanup(func() { _ = container.Terminate(context.Background()) })
	host, _ := container.Host(ctx)
	port, _ := container.MappedPort(ctx, "3306/tcp")
	return mysqlServer{addr: fmt.Sprintf("%s:%s", host, port.Port())}
}

func (s mysqlServer) open(t *testing.T, database string) *sql.DB {
	return s.openAs(t, database, "root", "root-pw")
}

func (s mysqlServer) openAs(t *testing.T, database, user, password string) *sql.DB {
	t.Helper()
	config := mysql.NewConfig()
	config.Net, config.Addr, config.User, config.Passwd, config.DBName = "tcp", s.addr, user, password, database
	config.TLSConfig = "skip-verify"
	connector, err := mysql.NewConnector(config)
	if err != nil {
		t.Fatal(err)
	}
	db := sql.OpenDB(connector)
	t.Cleanup(func() { _ = db.Close() })
	return db
}
