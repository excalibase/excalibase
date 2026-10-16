//go:build integration

package schema

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/go-sql-driver/mysql"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

const mysqlTestImage = "mysql:8.4"

// mysqlProject starts MySQL 8.4 with an app database and an excalibase_app
// login like a provisioned project's, and opens it the way projectdb does:
// over TLS, one pool for single statements and one for scripts.
func mysqlProject(t *testing.T) (single, script *sql.DB) {
	t.Helper()
	ctx := context.Background()
	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image:        mysqlTestImage,
			Env:          map[string]string{"MYSQL_ROOT_PASSWORD": "root-pw", "MYSQL_DATABASE": "app"},
			ExposedPorts: []string{"3306/tcp"},
			WaitingFor:   wait.ForLog("port: 3306  MySQL Community Server").WithStartupTimeout(3 * time.Minute),
		},
		Started: true,
	})
	if err != nil {
		t.Fatalf("start %s: %v", mysqlTestImage, err)
	}
	t.Cleanup(func() { _ = container.Terminate(context.Background()) })
	host, err := container.Host(ctx)
	if err != nil {
		t.Fatal(err)
	}
	port, err := container.MappedPort(ctx, "3306/tcp")
	if err != nil {
		t.Fatal(err)
	}
	address := fmt.Sprintf("%s:%s", host, port.Port())
	root := openMySQL(t, address, "root", "root-pw", false)
	for _, statement := range []string{
		"CREATE USER 'excalibase_app'@'%' IDENTIFIED BY 'app-pw' REQUIRE SSL",
		"GRANT ALL PRIVILEGES ON `app`.* TO 'excalibase_app'@'%'",
	} {
		if _, err := root.ExecContext(ctx, statement); err != nil {
			t.Fatalf("%s: %v", statement, err)
		}
	}
	return openMySQL(t, address, "excalibase_app", "app-pw", false), openMySQL(t, address, "excalibase_app", "app-pw", true)
}

func openMySQL(t *testing.T, address, user, password string, multiStatements bool) *sql.DB {
	t.Helper()
	config := mysql.NewConfig()
	config.Net, config.Addr, config.User, config.Passwd, config.DBName = "tcp", address, user, password, "app"
	config.TLSConfig = "skip-verify"
	config.MultiStatements = multiStatements
	config.Params = map[string]string{"max_execution_time": "30000", "lock_wait_timeout": "5", "innodb_lock_wait_timeout": "5"}
	connector, err := mysql.NewConnector(config)
	if err != nil {
		t.Fatal(err)
	}
	db := sql.OpenDB(connector)
	t.Cleanup(func() { _ = db.Close() })
	return db
}

const mysqlFixture = `
CREATE TABLE customers (
  id bigint unsigned auto_increment PRIMARY KEY,
  email varchar(255) NOT NULL UNIQUE,
  active tinyint(1) NOT NULL DEFAULT 1
);
CREATE TABLE orders (
  id bigint unsigned auto_increment PRIMARY KEY,
  customer_id bigint unsigned NOT NULL,
  status enum('draft','paid') NOT NULL DEFAULT 'draft',
  total decimal(10,2) NOT NULL,
  meta json,
  placed_at datetime DEFAULT CURRENT_TIMESTAMP,
  CONSTRAINT orders_customer FOREIGN KEY (customer_id) REFERENCES customers (id) ON DELETE CASCADE,
  CONSTRAINT orders_total_positive CHECK (total >= 0),
  INDEX orders_status_total (status, total)
);
INSERT INTO customers (email) VALUES ('ada@example.com'), ('bob@example.com');
INSERT INTO orders (customer_id, status, total) VALUES (1, 'paid', 12.50);
SELECT email, active FROM customers ORDER BY id`

func TestIntegration_MySQLCatalog(t *testing.T) {
	single, script := mysqlProject(t)
	ctx := context.Background()
	catalog := NewMySQLCatalog()

	seeded := catalog.ExecuteQuery(ctx, script, mysqlFixture)
	if seeded.Error != "" || len(seeded.Rows) != 2 || seeded.Rows[0][0] != "ada@example.com" {
		t.Fatalf("a script's last result set: %+v", seeded)
	}

	t.Run("tables of the one database", func(t *testing.T) {
		tables, err := catalog.GetTables(ctx, single, "public")
		if err != nil || len(tables) != 2 || tables[0].Name != "customers" || tables[0].Schema != "app" {
			t.Fatalf("tables %+v, %v", tables, err)
		}
		if _, err := catalog.GetTables(ctx, single, "app"); err != nil {
			t.Fatalf("by its own name: %v", err)
		}
		var input *InputError
		if _, err := catalog.GetTables(ctx, single, "auth"); !errors.As(err, &input) {
			t.Fatalf("another schema: %v", err)
		}
	})

	t.Run("columns carry the full MySQL type", func(t *testing.T) {
		columns, err := catalog.GetColumns(ctx, single, "public", "orders")
		if err != nil {
			t.Fatal(err)
		}
		byName := map[string]ColumnInfo{}
		for _, column := range columns {
			byName[column.Name] = column
		}
		if byName["status"].DataType != "enum('draft','paid')" || byName["meta"].DataType != "json" || !byName["meta"].Nullable {
			t.Errorf("columns %+v", byName)
		}
		if !byName["id"].PrimaryKey || byName["id"].DefaultValue == nil || *byName["id"].DefaultValue != "AUTO_INCREMENT" {
			t.Errorf("id %+v", byName["id"])
		}
		customers, _ := catalog.GetColumns(ctx, single, "public", "customers")
		if customers[1].Name != "email" || !customers[1].Unique || customers[2].DataType != "tinyint(1)" {
			t.Errorf("customers %+v", customers)
		}
	})

	t.Run("relationships, indexes and checks", func(t *testing.T) {
		relationships, err := catalog.GetRelationships(ctx, single, "public")
		if err != nil || len(relationships) != 1 || relationships[0] != (RelationshipInfo{
			ConstraintName: "orders_customer", SourceTable: "orders", SourceColumn: "customer_id",
			TargetTable: "customers", TargetColumn: "id", OnDelete: "CASCADE", OnUpdate: "NO ACTION",
		}) {
			t.Fatalf("relationships %+v, %v", relationships, err)
		}
		indexes, err := catalog.GetIndexes(ctx, single, "public", "orders")
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, index := range indexes {
			if index.Name == "orders_status_total" {
				found = !index.Unique && index.Type == "btree" && strings.Join(index.Columns, ",") == "status,total"
			}
		}
		if !found {
			t.Fatalf("indexes %+v", indexes)
		}
		checks, err := catalog.GetCheckConstraints(ctx, single, "public", "orders")
		if err != nil || len(checks) != 1 || checks[0].Name != "orders_total_positive" || strings.Join(checks[0].Columns, ",") != "total" {
			t.Fatalf("checks %+v, %v", checks, err)
		}
	})

	t.Run("table editor DDL", func(t *testing.T) {
		draft := "'draft'"
		if err := catalog.CreateTable(ctx, single, CreateTableRequest{Name: "notes", Schema: "public", Columns: []CreateColumnDef{
			{Name: "id", Type: "char(36)", PrimaryKey: true, Default: ptr("(UUID())")},
			{Name: "state", Type: "enum('draft','done')", Default: &draft},
		}}); err != nil {
			t.Fatal(err)
		}
		if err := catalog.AddColumn(ctx, single, "public", "notes", AddColumnRequest{Name: "body", Type: "text", Nullable: true}); err != nil {
			t.Fatal(err)
		}
		if err := catalog.CreateIndex(ctx, single, CreateIndexRequest{Name: "notes_state", Table: "notes", Columns: []string{"state"}}); err != nil {
			t.Fatal(err)
		}
		if err := catalog.DropIndex(ctx, single, "public", "notes_state"); err != nil {
			t.Fatal(err)
		}
		if err := catalog.DropColumn(ctx, single, "public", "notes", "body"); err != nil {
			t.Fatal(err)
		}
		inserted, err := catalog.InsertRow(ctx, single, "public", "notes", map[string]interface{}{})
		if err != nil || inserted.Command != "INSERT" || len(inserted.Rows) != 0 {
			t.Fatalf("a row keyed by an expression is not read back: %+v, %v", inserted, err)
		}
		if err := catalog.DropTable(ctx, single, "public", "notes", true); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("rows", func(t *testing.T) {
		inserted, err := catalog.InsertRow(ctx, single, "public", "customers", map[string]interface{}{"email": "cy@example.com"})
		if err != nil || len(inserted.Rows) != 1 || inserted.Rows[0][1] != "cy@example.com" {
			t.Fatalf("auto id read back: %+v, %v", inserted, err)
		}
		chosen, err := catalog.InsertRow(ctx, single, "public", "customers", map[string]interface{}{"id": 40, "email": "d@example.com"})
		if err != nil || len(chosen.Rows) != 1 {
			t.Fatalf("given id read back: %+v, %v", chosen, err)
		}
		if err := catalog.UpdateRow(ctx, single, "public", "customers", "id", "40", map[string]interface{}{"active": false}); err != nil {
			t.Fatal(err)
		}
		page, err := catalog.GetRows(ctx, single, "public", "customers", RowQueryOpts{
			Sort: "id", Order: "desc", Filters: []RowFilter{{Column: "email", Operator: "ilike", Value: "%EXAMPLE%"}},
		})
		if err != nil || page.TotalCount != 4 || len(page.Rows) != 4 || fmt.Sprint(page.Rows[0][2]) != "0" {
			t.Fatalf("page %+v, %v", page, err)
		}
		if err := catalog.DeleteRow(ctx, single, "public", "customers", "id", "40"); err != nil {
			t.Fatal(err)
		}
	})
}

func ptr(value string) *string { return &value }

// A failed script keeps what ran before the failure: MySQL DDL commits as it
// goes, which is why the runner and MCP say a migration is not rolled back.
func TestIntegration_MySQLScriptIsNotTransactional(t *testing.T) {
	single, script := mysqlProject(t)
	ctx := context.Background()
	catalog := NewMySQLCatalog()
	failed := catalog.ExecuteQuery(ctx, script, "CREATE TABLE first_step (a int); CREATE TABLE first_step (a int); CREATE TABLE never (a int)")
	if !strings.Contains(failed.Error, "MySQL error 1050") {
		t.Fatalf("error %+v", failed)
	}
	tables, _ := catalog.GetTables(ctx, single, "public")
	if len(tables) != 1 || tables[0].Name != "first_step" {
		t.Fatalf("tables after the failure: %+v", tables)
	}
	written := catalog.ExecuteQuery(ctx, script, "INSERT INTO first_step VALUES (1), (2); INSERT INTO first_step VALUES (3)")
	if written.Error != "" || written.AffectedRows != 3 {
		t.Fatalf("affected rows: %+v", written)
	}
}

// Read-only SQL reads, and nothing it is handed writes (EXC-544 on MySQL).
func TestIntegration_MySQLReadOnlyQuery(t *testing.T) {
	single, script := mysqlProject(t)
	ctx := context.Background()
	catalog := NewMySQLCatalog()
	if seeded := catalog.ExecuteQuery(ctx, script, "CREATE TABLE kv (k int PRIMARY KEY, v int); INSERT INTO kv VALUES (1, 1)"); seeded.Error != "" {
		t.Fatal(seeded.Error)
	}
	if read := catalog.ExecuteReadOnlyQuery(ctx, single, "SELECT v FROM kv"); read.Error != "" || len(read.Rows) != 1 {
		t.Fatalf("read %+v", read)
	}
	for _, statement := range []string{
		"INSERT INTO kv VALUES (2, 2)",
		"WITH x AS (SELECT 1) UPDATE kv SET v = 9",
		"SELECT 1; UPDATE kv SET v = 9",
		"SELECT * FROM kv INTO OUTFILE '/tmp/kv'",
	} {
		if result := catalog.ExecuteReadOnlyQuery(ctx, single, statement); result.Error == "" {
			t.Errorf("%q ran", statement)
		}
	}
	after := catalog.ExecuteReadOnlyQuery(ctx, single, "SELECT COUNT(*), MAX(v) FROM kv")
	if fmt.Sprint(after.Rows) != "[[1 1]]" {
		t.Fatalf("kv changed: %+v", after)
	}
}

// A statement past the deadline is stopped on the server, and nothing a
// script sets on its session reaches the next one.
func TestIntegration_MySQLRunnerBounds(t *testing.T) {
	single, script := mysqlProject(t)
	ctx := context.Background()
	catalog := NewMySQLCatalog().WithStatementTimeout(time.Second)
	started := time.Now()
	stopped := catalog.ExecuteQuery(ctx, script, "DO SLEEP(30)")
	if stopped.Error == "" || time.Since(started) > 10*time.Second {
		t.Fatalf("sleep: %+v after %s", stopped, time.Since(started))
	}
	var sleeping int
	if err := single.QueryRowContext(ctx, "SELECT COUNT(*) FROM information_schema.processlist WHERE info = 'DO SLEEP(30)'").Scan(&sleeping); err != nil || sleeping != 0 {
		t.Fatalf("the statement still runs: %d %v", sleeping, err)
	}
	if set := catalog.ExecuteQuery(ctx, script, "SET SESSION max_execution_time = 0"); set.Error != "" {
		t.Fatal(set.Error)
	}
	limit := catalog.ExecuteQuery(ctx, script, "SELECT @@max_execution_time")
	if fmt.Sprint(limit.Rows) != "[[30000]]" {
		t.Fatalf("session setting leaked: %+v", limit)
	}
}
