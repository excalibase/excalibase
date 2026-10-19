//go:build integration

package permissions

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/go-sql-driver/mysql"
	_ "github.com/lib/pq"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
)

// EXC-649: the Corner Shop user test, on the databases themselves.

const cornerShopMySQL = `
CREATE TABLE products (id bigint unsigned auto_increment PRIMARY KEY, name varchar(100) NOT NULL, price decimal(10,2) NOT NULL);
CREATE TABLE orders (id bigint unsigned auto_increment PRIMARY KEY, customer_id varchar(64) NOT NULL, status varchar(20) NOT NULL);
CREATE TABLE order_items (
  id bigint unsigned auto_increment PRIMARY KEY,
  order_id bigint unsigned NOT NULL, product_id bigint unsigned NOT NULL, qty int NOT NULL,
  CONSTRAINT items_order FOREIGN KEY (order_id) REFERENCES orders (id),
  CONSTRAINT items_product FOREIGN KEY (product_id) REFERENCES products (id));
CREATE VIEW product_stats AS SELECT product_id, SUM(qty) AS sold FROM order_items GROUP BY product_id`

const cornerShopPostgres = `
CREATE TABLE products (id bigserial PRIMARY KEY, name text NOT NULL, price numeric(10,2) NOT NULL);
CREATE TABLE orders (id bigserial PRIMARY KEY, customer_id text NOT NULL, status text NOT NULL);
CREATE TABLE order_items (
  id bigserial PRIMARY KEY,
  order_id bigint NOT NULL CONSTRAINT items_order REFERENCES orders (id),
  product_id bigint NOT NULL CONSTRAINT items_product REFERENCES products (id), qty int NOT NULL);
CREATE VIEW product_stats AS SELECT product_id, SUM(qty) AS sold FROM order_items GROUP BY product_id;
CREATE MATERIALIZED VIEW product_totals AS SELECT product_id, COUNT(*) AS lines FROM order_items GROUP BY product_id`

func TestIntegration_CheckLiveOnMySQL(t *testing.T) {
	db := startMySQL(t)
	source := func(ctx context.Context, schemaName string) (*LiveSchema, error) {
		return ReadMySQLSchema(ctx, db, schemaName)
	}
	expectCornerShop(t, source, "app")
	reason := refusal(t, source, "public.products", "select", `{"filter":{},"columns":"*"}`)
	if !strings.Contains(reason, "app.products") {
		t.Fatalf("public.products on MySQL: %q", reason)
	}
}

func TestIntegration_CheckLiveOnPostgres(t *testing.T) {
	db := startPostgres(t)
	source := func(ctx context.Context, schemaName string) (*LiveSchema, error) {
		return ReadPostgresSchema(ctx, db, schemaName)
	}
	expectCornerShop(t, source, "public")
	if reason := refusal(t, source, "public.product_totals", "select", `{"filter":{},"columns":["lines"]}`); reason != "" {
		t.Fatalf("a materialized view is served by the engine: %s", reason)
	}
}

func expectCornerShop(t *testing.T, source SchemaSource, schemaName string) {
	t.Helper()
	prefix := map[string]string{"app": "app", "public": "public"}[schemaName]
	for _, body := range []struct{ table, op, body string }{
		{"order_items", "select", `{"filter":{"` + prefix + `Order":{"status":{"_eq":"paid"}}},"columns":["id","qty"]}`},
		{"orders", "select", `{"filter":{"` + prefix + `OrderItems":{"qty":{"_gt":0}}},"columns":"*"}`},
		{"product_stats", "select", `{"filter":{},"columns":["sold"]}`},
	} {
		if reason := refusal(t, source, schemaName+"."+body.table, body.op, body.body); reason != "" {
			t.Errorf("%s %s refused: %s", body.table, body.body, reason)
		}
	}
	for _, c := range []struct{ table, op, body, want string }{
		{"nope", "select", `{"filter":{},"columns":"*"}`, "Unknown table " + schemaName + ".nope"},
		{"order_items", "select", `{"filter":{"orders":{"status":{"_eq":"paid"}}},"columns":"*"}`, "Unknown relationship orders"},
		{"products", "select", `{"filter":{},"columns":["colour"]}`, "Unknown column colour"},
		{"product_stats", "update", `{"filter":{},"columns":"*"}`, "is a view"},
	} {
		if reason := refusal(t, source, schemaName+"."+c.table, c.op, c.body); !strings.Contains(reason, c.want) {
			t.Errorf("%s %s: reason %q, want %q", c.table, c.body, reason, c.want)
		}
	}
}

func refusal(t *testing.T, source SchemaSource, table, op, body string) string {
	t.Helper()
	err := CheckLive(context.Background(), source, table, op, json.RawMessage(body))
	if err == nil {
		return ""
	}
	var invalid *InvalidPermissionError
	if !errors.As(err, &invalid) {
		t.Fatalf("%s: %v", table, err)
	}
	return invalid.Reason
}

func startMySQL(t *testing.T) *sql.DB {
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
	config := mysql.NewConfig()
	config.Net, config.Addr, config.User, config.Passwd, config.DBName = "tcp", fmt.Sprintf("%s:%s", host, port.Port()), "root", "root-pw", "app"
	config.MultiStatements = true
	connector, err := mysql.NewConnector(config)
	if err != nil {
		t.Fatal(err)
	}
	db := sql.OpenDB(connector)
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.ExecContext(ctx, cornerShopMySQL); err != nil {
		t.Fatalf("seed: %v", err)
	}
	return db
}

func startPostgres(t *testing.T) *sql.DB {
	t.Helper()
	ctx := context.Background()
	container, err := postgres.Run(ctx, "postgres:16-alpine",
		postgres.WithDatabase("app"), postgres.WithUsername("app"), postgres.WithPassword("app-pw"),
		testcontainers.WithWaitStrategy(wait.ForLog("database system is ready to accept connections").WithOccurrence(2).WithStartupTimeout(time.Minute)))
	if err != nil {
		t.Fatalf("start postgres: %v", err)
	}
	t.Cleanup(func() { _ = container.Terminate(context.Background()) })
	dsn, err := container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.ExecContext(ctx, cornerShopPostgres); err != nil {
		t.Fatalf("seed: %v", err)
	}
	return db
}
