//go:build integration

package schema

import (
	"context"
	"database/sql"
	"fmt"
	"testing"
	"time"

	_ "github.com/lib/pq"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
)

func setupPG(t *testing.T) (*sql.DB, *sql.DB, func()) {
	t.Helper()
	ctx := context.Background()

	pgContainer, err := postgres.Run(ctx, "postgres:16-alpine",
		postgres.WithDatabase("testdb"),
		postgres.WithUsername("superuser"),
		postgres.WithPassword("superpass"),
		testcontainers.WithWaitStrategy(
			wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2).WithStartupTimeout(30*time.Second),
		),
	)
	if err != nil {
		t.Fatalf("start postgres: %v", err)
	}

	connStr, _ := pgContainer.ConnectionString(ctx, "sslmode=disable")

	// Connect as superuser to create tables and roles
	superDB, err := sql.Open("postgres", connStr)
	if err != nil {
		t.Fatalf("connect super: %v", err)
	}

	// Create tables as superuser (mimics provisioning creating tables as postgres)
	_, err = superDB.ExecContext(ctx, `
		CREATE TABLE users (
			id serial PRIMARY KEY,
			email varchar(100) UNIQUE NOT NULL,
			full_name varchar(100),
			created_at timestamptz DEFAULT now()
		);
		CREATE TABLE orders (
			id serial PRIMARY KEY,
			user_id int NOT NULL REFERENCES users(id),
			total numeric(10,2) NOT NULL,
			status varchar(20) DEFAULT 'pending'
		);
		CREATE TABLE products (
			id serial PRIMARY KEY,
			name varchar(200) NOT NULL,
			price numeric(10,2) NOT NULL
		);
		CREATE TABLE order_items (
			id serial PRIMARY KEY,
			order_id int NOT NULL REFERENCES orders(id),
			product_id int NOT NULL REFERENCES products(id),
			quantity int DEFAULT 1
		);

		-- Create excalibase_app role (non-superuser)
		CREATE ROLE excalibase_app WITH LOGIN PASSWORD 'apppass';
		GRANT ALL ON SCHEMA public TO excalibase_app;
		GRANT ALL ON ALL TABLES IN SCHEMA public TO excalibase_app;
		GRANT ALL ON ALL SEQUENCES IN SCHEMA public TO excalibase_app;
		ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT ALL ON TABLES TO excalibase_app;
	`)
	if err != nil {
		t.Fatalf("setup tables: %v", err)
	}

	// Connect as excalibase_app (non-superuser)
	host, _ := pgContainer.Host(ctx)
	port, _ := pgContainer.MappedPort(ctx, "5432/tcp")
	appConnStr := fmt.Sprintf("host=%s port=%s user=excalibase_app password=apppass dbname=testdb sslmode=disable", host, port.Port())
	appDB, err := sql.Open("postgres", appConnStr)
	if err != nil {
		t.Fatalf("connect app: %v", err)
	}

	cleanup := func() {
		appDB.Close()
		superDB.Close()
		pgContainer.Terminate(ctx)
	}
	return superDB, appDB, cleanup
}

func TestIntegration_GetTables_AsNonSuperuser(t *testing.T) {
	_, appDB, cleanup := setupPG(t)
	defer cleanup()

	introspector := NewIntrospector()
	tables, err := introspector.GetTables(context.Background(), appDB, "public")
	if err != nil {
		t.Fatalf("GetTables: %v", err)
	}
	if len(tables) != 4 {
		t.Fatalf("expected 4 tables, got %d", len(tables))
	}

	names := map[string]bool{}
	for _, t := range tables {
		names[t.Name] = true
	}
	for _, expected := range []string{"users", "orders", "products", "order_items"} {
		if !names[expected] {
			t.Errorf("missing table: %s", expected)
		}
	}
}

func TestIntegration_GetColumns_AsNonSuperuser(t *testing.T) {
	_, appDB, cleanup := setupPG(t)
	defer cleanup()

	introspector := NewIntrospector()
	cols, err := introspector.GetColumns(context.Background(), appDB, "public", "users")
	if err != nil {
		t.Fatalf("GetColumns: %v", err)
	}
	if len(cols) != 4 {
		t.Fatalf("expected 4 columns, got %d", len(cols))
	}

	// id should be PK
	var idCol ColumnInfo
	for _, c := range cols {
		if c.Name == "id" {
			idCol = c
			break
		}
	}
	if !idCol.PrimaryKey {
		t.Error("id should be primary key")
	}

	// email should be unique
	var emailCol ColumnInfo
	for _, c := range cols {
		if c.Name == "email" {
			emailCol = c
			break
		}
	}
	if !emailCol.Unique {
		t.Error("email should be unique")
	}
	if emailCol.Nullable {
		t.Error("email should not be nullable")
	}
}

func TestIntegration_GetRelationships_AsNonSuperuser(t *testing.T) {
	_, appDB, cleanup := setupPG(t)
	defer cleanup()

	introspector := NewIntrospector()
	rels, err := introspector.GetRelationships(context.Background(), appDB, "public")
	if err != nil {
		t.Fatalf("GetRelationships: %v", err)
	}
	if len(rels) != 3 {
		t.Fatalf("expected 3 FKs, got %d: %+v", len(rels), rels)
	}

	fkMap := map[string]string{}
	for _, r := range rels {
		fkMap[r.SourceTable+"."+r.SourceColumn] = r.TargetTable + "." + r.TargetColumn
	}

	expected := map[string]string{
		"orders.user_id":         "users.id",
		"order_items.order_id":   "orders.id",
		"order_items.product_id": "products.id",
	}
	for src, tgt := range expected {
		if fkMap[src] != tgt {
			t.Errorf("FK %s -> expected %s, got %s", src, tgt, fkMap[src])
		}
	}
}

func TestIntegration_GetIndexes_AsNonSuperuser(t *testing.T) {
	_, appDB, cleanup := setupPG(t)
	defer cleanup()

	introspector := NewIntrospector()
	indexes, err := introspector.GetIndexes(context.Background(), appDB, "public", "users")
	if err != nil {
		t.Fatalf("GetIndexes: %v", err)
	}
	if len(indexes) == 0 {
		t.Fatal("expected at least 1 index (PK)")
	}

	hasPK := false
	for _, idx := range indexes {
		if idx.Unique && len(idx.Columns) > 0 && idx.Columns[0] == "id" {
			hasPK = true
		}
	}
	if !hasPK {
		t.Error("expected PK index on id")
	}
}

func TestIntegration_ExecuteDDL_AsNonSuperuser(t *testing.T) {
	_, appDB, cleanup := setupPG(t)
	defer cleanup()

	introspector := NewIntrospector()
	result := introspector.ExecuteDDL(context.Background(), appDB, "CREATE TABLE test_ddl (id serial PRIMARY KEY, name text)")
	if !result.Success {
		t.Fatalf("DDL failed: %s", result.Error)
	}

	// Verify table was created
	tables, _ := introspector.GetTables(context.Background(), appDB, "public")
	found := false
	for _, tbl := range tables {
		if tbl.Name == "test_ddl" {
			found = true
		}
	}
	if !found {
		t.Error("test_ddl table not created")
	}
}

func TestIntegration_TestConnection_AsNonSuperuser(t *testing.T) {
	_, appDB, cleanup := setupPG(t)
	defer cleanup()

	introspector := NewIntrospector()
	if !introspector.TestConnection(context.Background(), appDB) {
		t.Error("connection test should pass")
	}
}

func TestIntegration_ExecuteQuery_SELECT(t *testing.T) {
	_, appDB, cleanup := setupPG(t)
	defer cleanup()

	introspector := NewIntrospector()
	ctx := context.Background()

	// Insert test data
	_, err := appDB.ExecContext(ctx, "INSERT INTO users (email, full_name) VALUES ('test@example.com', 'Test User')")
	if err != nil {
		t.Fatalf("insert: %v", err)
	}

	result := introspector.ExecuteQuery(ctx, appDB, "SELECT id, email, full_name FROM users WHERE email = 'test@example.com'")
	if result.Error != "" {
		t.Fatalf("query error: %s", result.Error)
	}
	if len(result.Columns) != 3 {
		t.Fatalf("expected 3 columns, got %d", len(result.Columns))
	}
	if result.Columns[0].Name != "id" {
		t.Errorf("expected column 'id', got '%s'", result.Columns[0].Name)
	}
	if result.Columns[1].Name != "email" {
		t.Errorf("expected column 'email', got '%s'", result.Columns[1].Name)
	}
	if len(result.Rows) != 1 {
		t.Fatalf("expected 1 row, got %d", len(result.Rows))
	}
	if result.Rows[0][1] != "test@example.com" {
		t.Errorf("expected 'test@example.com', got '%v'", result.Rows[0][1])
	}
}

func TestIntegration_ExecuteQuery_DML(t *testing.T) {
	_, appDB, cleanup := setupPG(t)
	defer cleanup()

	introspector := NewIntrospector()
	ctx := context.Background()

	result := introspector.ExecuteQuery(ctx, appDB, "INSERT INTO users (email, full_name) VALUES ('dml@test.com', 'DML Test')")
	if result.Error != "" {
		t.Fatalf("insert error: %s", result.Error)
	}
	if result.Command != "EXEC" {
		t.Errorf("expected command 'EXEC', got '%s'", result.Command)
	}
	if result.AffectedRows != 1 {
		t.Errorf("expected 1 affected row, got %d", result.AffectedRows)
	}
}

func TestIntegration_ExecuteQuery_DDL(t *testing.T) {
	_, appDB, cleanup := setupPG(t)
	defer cleanup()

	introspector := NewIntrospector()
	ctx := context.Background()

	result := introspector.ExecuteQuery(ctx, appDB, "CREATE TABLE query_test (id serial PRIMARY KEY, value text)")
	if result.Error != "" {
		t.Fatalf("DDL error: %s", result.Error)
	}
	if result.Command != "EXEC" {
		t.Errorf("expected command 'EXEC', got '%s'", result.Command)
	}
}

func TestIntegration_ExecuteQuery_InvalidSQL(t *testing.T) {
	_, appDB, cleanup := setupPG(t)
	defer cleanup()

	introspector := NewIntrospector()
	ctx := context.Background()

	result := introspector.ExecuteQuery(ctx, appDB, "SELECT * FROM nonexistent_table_xyz")
	if result.Error == "" {
		t.Fatal("expected error for invalid SQL")
	}
}

func TestIntegration_ExecuteQuery_EXPLAIN(t *testing.T) {
	_, appDB, cleanup := setupPG(t)
	defer cleanup()

	introspector := NewIntrospector()
	ctx := context.Background()

	result := introspector.ExecuteQuery(ctx, appDB, "EXPLAIN SELECT * FROM users")
	if result.Error != "" {
		t.Fatalf("EXPLAIN error: %s", result.Error)
	}
	if len(result.Columns) == 0 {
		t.Error("expected columns from EXPLAIN")
	}
	if len(result.Rows) == 0 {
		t.Error("expected rows from EXPLAIN")
	}
}

func TestIntegration_ExecuteQuery_WITH(t *testing.T) {
	_, appDB, cleanup := setupPG(t)
	defer cleanup()

	introspector := NewIntrospector()
	ctx := context.Background()

	_, err := appDB.ExecContext(ctx, "INSERT INTO users (email, full_name) VALUES ('cte@test.com', 'CTE User')")
	if err != nil {
		t.Fatalf("insert: %v", err)
	}

	result := introspector.ExecuteQuery(ctx, appDB, "WITH u AS (SELECT * FROM users) SELECT email FROM u")
	if result.Error != "" {
		t.Fatalf("CTE error: %s", result.Error)
	}
	if len(result.Rows) == 0 {
		t.Error("expected rows from CTE query")
	}
}
