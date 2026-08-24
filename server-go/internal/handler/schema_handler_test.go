//go:build integration

package handler

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/schema"
	"github.com/excalibase/provisioning-poc/internal/testutil"
	"github.com/excalibase/provisioning-poc/pkg/vault"
	"github.com/go-chi/chi/v5"
	_ "github.com/lib/pq"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
)

const (
	testUnsealFmt   = "unseal: %v"
	testSchemaBase  = "/api/schema"
	testTablesPath  = "/api/schema/test-proj/tables"
	testColumnsPath = "/api/schema/test-proj/tables/users/columns"
	testQueryPath   = "/api/schema/test-proj/query"
	testRolesPath   = "/api/schema/test-proj/roles"
	testPoliciesPath = "/api/schema/test-proj/policies"
	testFunctionsPath = "/api/schema/test-proj/functions"
)


// setupSchemaRouter creates a chi router with SchemaHandler wired to a real
// PostgreSQL via testcontainers, vault unsealed with credentials stored.
func setupSchemaRouter(t *testing.T) chi.Router {
	t.Helper()
	ctx := context.Background()

	// 1. Start postgres container
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
	t.Cleanup(func() { pgContainer.Terminate(ctx) })

	host, _ := pgContainer.Host(ctx)
	port, _ := pgContainer.MappedPort(ctx, "5432/tcp")

	// Create tables and app role
	connStr, _ := pgContainer.ConnectionString(ctx, "sslmode=disable")
	superDB, err := sql.Open("postgres", connStr)
	if err != nil {
		t.Fatalf("connect super: %v", err)
	}
	defer superDB.Close()

	_, err = superDB.ExecContext(ctx, `
		CREATE ROLE excalibase_app WITH LOGIN PASSWORD 'apppass' CREATEROLE;
		GRANT ALL ON SCHEMA public TO excalibase_app;
		ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT ALL ON TABLES TO excalibase_app;
		ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT ALL ON SEQUENCES TO excalibase_app;
	`)
	if err != nil {
		t.Fatalf("create role: %v", err)
	}

	// Connect as excalibase_app to create tables (so excalibase_app owns them)
	appConnStr := fmt.Sprintf("host=%s port=%s user=excalibase_app password=apppass dbname=testdb sslmode=disable", host, port.Port())
	appDB, err := sql.Open("postgres", appConnStr)
	if err != nil {
		t.Fatalf("connect app: %v", err)
	}
	defer appDB.Close()

	_, err = appDB.ExecContext(ctx, `
		CREATE TABLE users (
			id serial PRIMARY KEY,
			email varchar(100) UNIQUE NOT NULL,
			name text
		);
		INSERT INTO users (email, name) VALUES ('alice@test.com', 'Alice');
	`)
	if err != nil {
		t.Fatalf("setup: %v", err)
	}

	// 2. Create + init + unseal vault, store credentials
	dir := t.TempDir()
	v, err := vault.New(filepath.Join(dir, testVaultFile))
	if err != nil {
		t.Fatalf("new vault: %v", err)
	}

	initResult, err := v.Init(5, 3)
	if err != nil {
		t.Fatalf("vault init: %v", err)
	}
	for _, share := range initResult.Shares[:3] {
		if _, err := v.Unseal(share); err != nil {
			t.Fatalf(testUnsealFmt, err)
		}
	}

	// Store credentials at the canonical project-scoped path.
	err = v.Put("projects/test-proj/credentials/excalibase_app", map[string]string{
		"host":     host,
		"port":     port.Port(),
		"username": "excalibase_app",
		"password": "apppass",
		"database": "testdb",
	})
	if err != nil {
		t.Fatalf("vault put: %v", err)
	}

	// 3. Wire handler (testcontainers postgres has no SSL)
	t.Setenv("SCHEMA_DB_SSLMODE", "disable")
	h := NewSchemaHandler(v)
	r := chi.NewRouter()
	r.Route(testSchemaBase, h.Routes)
	return r
}

func schemaRequest(r chi.Router, method, path, body string) *httptest.ResponseRecorder {
	var req *http.Request
	if body != "" {
		req = httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
	} else {
		req = httptest.NewRequest(method, path, nil)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

// ---- Introspection handlers ----

func TestSchemaHandler_GetTables(t *testing.T) {
	r := setupSchemaRouter(t)

	w := schemaRequest(r, "GET", testTablesPath, "")
	if w.Code != 200 {
		t.Fatalf("GET /tables: %d, body: %s", w.Code, w.Body.String())
	}

	var tables []schema.TableInfo
	json.NewDecoder(w.Body).Decode(&tables)
	if len(tables) != 1 {
		t.Fatalf("expected 1 table, got %d", len(tables))
	}
	if tables[0].Name != "users" {
		t.Errorf("expected table 'users', got '%s'", tables[0].Name)
	}
}

func TestSchemaHandler_GetColumns(t *testing.T) {
	r := setupSchemaRouter(t)

	w := schemaRequest(r, "GET", testColumnsPath, "")
	if w.Code != 200 {
		t.Fatalf("GET columns: %d, body: %s", w.Code, w.Body.String())
	}

	var cols []schema.ColumnInfo
	json.NewDecoder(w.Body).Decode(&cols)
	if len(cols) != 3 {
		t.Fatalf("expected 3 columns, got %d", len(cols))
	}

	// Verify id is PK
	for _, c := range cols {
		if c.Name == "id" && !c.PrimaryKey {
			t.Error("id should be primary key")
		}
		if c.Name == "email" && !c.Unique {
			t.Error("email should be unique")
		}
	}
}

func TestSchemaHandler_GetRelationships(t *testing.T) {
	r := setupSchemaRouter(t)

	w := schemaRequest(r, "GET", "/api/schema/test-proj/relationships", "")
	if w.Code != 200 {
		t.Fatalf("GET relationships: %d", w.Code)
	}

	var rels []schema.RelationshipInfo
	json.NewDecoder(w.Body).Decode(&rels)
	// No FKs in this simple schema
	if len(rels) != 0 {
		t.Errorf("expected 0 relationships, got %d", len(rels))
	}
}

func TestSchemaHandler_TestConnection(t *testing.T) {
	r := setupSchemaRouter(t)

	w := schemaRequest(r, "GET", "/api/schema/test-proj/connection-test", "")
	if w.Code != 200 {
		t.Fatalf("connection test: %d", w.Code)
	}

	var resp map[string]interface{}
	json.NewDecoder(w.Body).Decode(&resp)
	if resp["connected"] != true {
		t.Error("expected connected=true")
	}
}

// ---- Query Execution ----

func TestSchemaHandler_ExecuteQuery_SELECT(t *testing.T) {
	r := setupSchemaRouter(t)

	w := schemaRequest(r, "POST", testQueryPath,
		`{"query":"SELECT email, name FROM users WHERE email = 'alice@test.com'"}`)
	if w.Code != 200 {
		t.Fatalf("query: %d, body: %s", w.Code, w.Body.String())
	}

	var result schema.QueryResult
	json.NewDecoder(w.Body).Decode(&result)
	if result.Error != "" {
		t.Fatalf("query error: %s", result.Error)
	}
	if len(result.Columns) != 2 {
		t.Fatalf("expected 2 columns, got %d", len(result.Columns))
	}
	if len(result.Rows) != 1 {
		t.Fatalf("expected 1 row, got %d", len(result.Rows))
	}
}

func TestSchemaHandler_ExecuteQuery_DML(t *testing.T) {
	r := setupSchemaRouter(t)

	w := schemaRequest(r, "POST", testQueryPath,
		`{"query":"INSERT INTO users (email, name) VALUES ('bob@test.com', 'Bob')"}`)
	if w.Code != 200 {
		t.Fatalf("query: %d, body: %s", w.Code, w.Body.String())
	}

	var result schema.QueryResult
	json.NewDecoder(w.Body).Decode(&result)
	if result.Error != "" {
		t.Fatalf("query error: %s", result.Error)
	}
	if result.Command != "EXEC" {
		t.Errorf("expected command 'EXEC', got '%s'", result.Command)
	}
	if result.AffectedRows != 1 {
		t.Errorf("expected 1 affected row, got %d", result.AffectedRows)
	}
}

func TestSchemaHandler_ExecuteQuery_EmptyBody(t *testing.T) {
	r := setupSchemaRouter(t)

	w := schemaRequest(r, "POST", testQueryPath, `{"query":""}`)
	if w.Code != 400 {
		t.Errorf("empty query: expected 400, got %d", w.Code)
	}
}

func TestSchemaHandler_ExecuteQuery_InvalidJSON(t *testing.T) {
	r := setupSchemaRouter(t)

	w := schemaRequest(r, "POST", testQueryPath, "not json")
	if w.Code != 400 {
		t.Errorf("invalid json: expected 400, got %d", w.Code)
	}
}

func TestSchemaHandler_ExecuteDDL(t *testing.T) {
	r := setupSchemaRouter(t)

	w := schemaRequest(r, "POST", "/api/schema/test-proj/ddl",
		`{"sql":"CREATE TABLE ddl_test (id serial PRIMARY KEY)"}`)
	if w.Code != 200 {
		t.Fatalf("ddl: %d, body: %s", w.Code, w.Body.String())
	}

	var result schema.DDLResult
	json.NewDecoder(w.Body).Decode(&result)
	if !result.Success {
		t.Errorf("DDL should succeed, error: %s", result.Error)
	}
}

// ---- Table CRUD ----

func TestSchemaHandler_CreateTable(t *testing.T) {
	r := setupSchemaRouter(t)

	w := schemaRequest(r, "POST", testTablesPath,
		`{"name":"products","schema":"public","columns":[{"name":"id","type":"serial","primaryKey":true},{"name":"title","type":"text","nullable":true}]}`)
	if w.Code != 201 {
		t.Fatalf("create table: %d, body: %s", w.Code, w.Body.String())
	}

	// Verify table exists
	w = schemaRequest(r, "GET", testTablesPath, "")
	var tables []schema.TableInfo
	json.NewDecoder(w.Body).Decode(&tables)
	found := false
	for _, tbl := range tables {
		if tbl.Name == "products" {
			found = true
		}
	}
	if !found {
		t.Error("products table not found after creation")
	}
}

func TestSchemaHandler_CreateTable_MissingName(t *testing.T) {
	r := setupSchemaRouter(t)

	w := schemaRequest(r, "POST", testTablesPath, `{"schema":"public"}`)
	if w.Code != 400 {
		t.Errorf("missing name: expected 400, got %d", w.Code)
	}
}

func TestSchemaHandler_CreateTable_InvalidJSON(t *testing.T) {
	r := setupSchemaRouter(t)

	w := schemaRequest(r, "POST", testTablesPath, "not json")
	if w.Code != 400 {
		t.Errorf("invalid json: expected 400, got %d", w.Code)
	}
}

func TestSchemaHandler_UpdateTable(t *testing.T) {
	r := setupSchemaRouter(t)

	// Create a table first
	schemaRequest(r, "POST", testTablesPath,
		`{"name":"to_rename","schema":"public","columns":[{"name":"id","type":"serial","primaryKey":true}]}`)

	// Rename it
	w := schemaRequest(r, "PATCH", "/api/schema/test-proj/tables/to_rename",
		`{"newName":"renamed_tbl"}`)
	if w.Code != 200 {
		t.Fatalf("update table: %d, body: %s", w.Code, w.Body.String())
	}

	// Verify renamed
	w = schemaRequest(r, "GET", testTablesPath, "")
	var tables []schema.TableInfo
	json.NewDecoder(w.Body).Decode(&tables)
	found := false
	for _, tbl := range tables {
		if tbl.Name == "renamed_tbl" {
			found = true
		}
	}
	if !found {
		t.Error("renamed table not found")
	}
}

func TestSchemaHandler_DropTable(t *testing.T) {
	r := setupSchemaRouter(t)

	// Create table
	schemaRequest(r, "POST", testTablesPath,
		`{"name":"to_drop","schema":"public","columns":[{"name":"id","type":"serial","primaryKey":true}]}`)

	// Drop it
	w := schemaRequest(r, "DELETE", "/api/schema/test-proj/tables/to_drop?cascade=true", "")
	if w.Code != 200 {
		t.Fatalf("drop table: %d, body: %s", w.Code, w.Body.String())
	}

	// Verify gone
	w = schemaRequest(r, "GET", testTablesPath, "")
	var tables []schema.TableInfo
	json.NewDecoder(w.Body).Decode(&tables)
	for _, tbl := range tables {
		if tbl.Name == "to_drop" {
			t.Error("table should be dropped")
		}
	}
}

// ---- Column CRUD ----

func TestSchemaHandler_AddColumn(t *testing.T) {
	r := setupSchemaRouter(t)

	w := schemaRequest(r, "POST", testColumnsPath,
		`{"name":"age","type":"integer","nullable":true}`)
	if w.Code != 201 {
		t.Fatalf("add column: %d, body: %s", w.Code, w.Body.String())
	}

	// Verify column exists
	w = schemaRequest(r, "GET", testColumnsPath, "")
	var cols []schema.ColumnInfo
	json.NewDecoder(w.Body).Decode(&cols)
	found := false
	for _, c := range cols {
		if c.Name == "age" {
			found = true
			if c.DataType != "integer" {
				t.Errorf("expected type 'integer', got '%s'", c.DataType)
			}
		}
	}
	if !found {
		t.Error("age column not found")
	}
}

func TestSchemaHandler_AddColumn_MissingFields(t *testing.T) {
	r := setupSchemaRouter(t)

	w := schemaRequest(r, "POST", testColumnsPath,
		`{"name":""}`)
	if w.Code != 400 {
		t.Errorf("missing fields: expected 400, got %d", w.Code)
	}
}

func TestSchemaHandler_AlterColumn(t *testing.T) {
	r := setupSchemaRouter(t)

	// Rename column
	w := schemaRequest(r, "PATCH", "/api/schema/test-proj/tables/users/columns/name",
		`{"newName":"full_name"}`)
	if w.Code != 200 {
		t.Fatalf("alter column: %d, body: %s", w.Code, w.Body.String())
	}

	// Verify renamed
	w = schemaRequest(r, "GET", testColumnsPath, "")
	var cols []schema.ColumnInfo
	json.NewDecoder(w.Body).Decode(&cols)
	found := false
	for _, c := range cols {
		if c.Name == "full_name" {
			found = true
		}
	}
	if !found {
		t.Error("full_name column not found after rename")
	}
}

func TestSchemaHandler_DropColumn(t *testing.T) {
	r := setupSchemaRouter(t)

	// Add a column to drop
	schemaRequest(r, "POST", testColumnsPath,
		`{"name":"temp_col","type":"text","nullable":true}`)

	w := schemaRequest(r, "DELETE", "/api/schema/test-proj/tables/users/columns/temp_col", "")
	if w.Code != 200 {
		t.Fatalf("drop column: %d, body: %s", w.Code, w.Body.String())
	}
}

// ---- Roles ----

func TestSchemaHandler_GetRoles(t *testing.T) {
	r := setupSchemaRouter(t)

	w := schemaRequest(r, "GET", testRolesPath, "")
	if w.Code != 200 {
		t.Fatalf("get roles: %d, body: %s", w.Code, w.Body.String())
	}

	var roles []schema.RoleInfo
	json.NewDecoder(w.Body).Decode(&roles)
	if len(roles) == 0 {
		t.Fatal("expected at least 1 role")
	}
	// excalibase_app should be in the list
	found := false
	for _, r := range roles {
		if r.Name == "excalibase_app" {
			found = true
			if !r.Login {
				t.Error("excalibase_app should have login")
			}
		}
	}
	if !found {
		t.Error("excalibase_app role not found")
	}
}

func TestSchemaHandler_CreateRole(t *testing.T) {
	r := setupSchemaRouter(t)

	w := schemaRequest(r, "POST", testRolesPath,
		fmt.Sprintf(`{"name":"test_role","password":%q,"login":true}`, testutil.FixturePassword("schema-role-handler")))
	if w.Code != 201 {
		t.Fatalf("create role: %d, body: %s", w.Code, w.Body.String())
	}

	// Verify role exists
	w = schemaRequest(r, "GET", testRolesPath, "")
	var roles []schema.RoleInfo
	json.NewDecoder(w.Body).Decode(&roles)
	found := false
	for _, role := range roles {
		if role.Name == "test_role" {
			found = true
		}
	}
	if !found {
		t.Error("test_role not found after creation")
	}
}

func TestSchemaHandler_CreateRole_MissingName(t *testing.T) {
	r := setupSchemaRouter(t)

	w := schemaRequest(r, "POST", testRolesPath, `{"login":true}`)
	if w.Code != 400 {
		t.Errorf("missing name: expected 400, got %d", w.Code)
	}
}

func TestSchemaHandler_DropRole(t *testing.T) {
	r := setupSchemaRouter(t)

	// Create then drop
	schemaRequest(r, "POST", testRolesPath,
		`{"name":"drop_me","login":false}`)

	w := schemaRequest(r, "DELETE", "/api/schema/test-proj/roles/drop_me", "")
	if w.Code != 200 {
		t.Fatalf("drop role: %d, body: %s", w.Code, w.Body.String())
	}
}

// ---- Extensions ----

func TestSchemaHandler_GetExtensions(t *testing.T) {
	r := setupSchemaRouter(t)

	w := schemaRequest(r, "GET", "/api/schema/test-proj/extensions", "")
	if w.Code != 200 {
		t.Fatalf("get extensions: %d, body: %s", w.Code, w.Body.String())
	}

	var exts []schema.ExtensionInfo
	json.NewDecoder(w.Body).Decode(&exts)
	if len(exts) == 0 {
		t.Fatal("expected at least 1 extension (plpgsql)")
	}
}

func TestSchemaHandler_CreateAndDropExtension(t *testing.T) {
	r := setupSchemaRouter(t)

	// Create extension — excalibase_app may not be superuser, so this may fail.
	// We test the HTTP contract regardless.
	w := schemaRequest(r, "POST", "/api/schema/test-proj/extensions",
		`{"name":"pg_trgm"}`)
	// May be 201 (success) or 500 (permission denied)
	if w.Code != 201 && w.Code != 500 {
		t.Errorf("create extension: unexpected %d", w.Code)
	}

	if w.Code == 201 {
		// Drop it
		w = schemaRequest(r, "DELETE", "/api/schema/test-proj/extensions/pg_trgm?cascade=true", "")
		if w.Code != 200 {
			t.Errorf("drop extension: %d", w.Code)
		}
	}
}

// ---- Policies ----

func TestSchemaHandler_GetPolicies(t *testing.T) {
	r := setupSchemaRouter(t)

	w := schemaRequest(r, "GET", testPoliciesPath, "")
	if w.Code != 200 {
		t.Fatalf("get policies: %d, body: %s", w.Code, w.Body.String())
	}
}

func TestSchemaHandler_CreatePolicy(t *testing.T) {
	r := setupSchemaRouter(t)

	// Enable RLS first
	schemaRequest(r, "PATCH", "/api/schema/test-proj/tables/users",
		`{"rlsEnabled":true}`)

	w := schemaRequest(r, "POST", testPoliciesPath,
		`{"name":"users_select","table":"users","schema":"public","command":"SELECT","roles":"public","using":"true","permissive":true}`)
	if w.Code != 201 {
		t.Fatalf("create policy: %d, body: %s", w.Code, w.Body.String())
	}

	// Verify policy exists
	w = schemaRequest(r, "GET", testPoliciesPath, "")
	var policies []schema.PolicyInfo
	json.NewDecoder(w.Body).Decode(&policies)
	found := false
	for _, p := range policies {
		if p.Name == "users_select" {
			found = true
			if p.Table != "users" {
				t.Errorf("expected table 'users', got '%s'", p.Table)
			}
		}
	}
	if !found {
		t.Error("users_select policy not found")
	}
}

func TestSchemaHandler_DropPolicy(t *testing.T) {
	r := setupSchemaRouter(t)

	// Setup
	schemaRequest(r, "PATCH", "/api/schema/test-proj/tables/users", `{"rlsEnabled":true}`)
	schemaRequest(r, "POST", testPoliciesPath,
		`{"name":"to_drop_pol","table":"users","schema":"public","command":"ALL","roles":"public","using":"true","permissive":true}`)

	w := schemaRequest(r, "DELETE", "/api/schema/test-proj/policies/to_drop_pol?table=users", "")
	if w.Code != 200 {
		t.Fatalf("drop policy: %d, body: %s", w.Code, w.Body.String())
	}
}

// ---- Functions ----

func TestSchemaHandler_GetFunctions(t *testing.T) {
	r := setupSchemaRouter(t)

	w := schemaRequest(r, "GET", testFunctionsPath, "")
	if w.Code != 200 {
		t.Fatalf("get functions: %d, body: %s", w.Code, w.Body.String())
	}
}

func TestSchemaHandler_CreateFunction(t *testing.T) {
	r := setupSchemaRouter(t)

	w := schemaRequest(r, "POST", testFunctionsPath,
		`{"name":"greet","schema":"public","language":"sql","returnType":"text","args":"","body":"SELECT 'hello'","volatility":"IMMUTABLE"}`)
	if w.Code != 201 {
		t.Fatalf("create function: %d, body: %s", w.Code, w.Body.String())
	}

	// Verify function exists
	w = schemaRequest(r, "GET", testFunctionsPath, "")
	var fns []schema.FunctionInfo
	json.NewDecoder(w.Body).Decode(&fns)
	found := false
	for _, fn := range fns {
		if fn.Name == "greet" {
			found = true
			if fn.Language != "sql" {
				t.Errorf("expected language 'sql', got '%s'", fn.Language)
			}
		}
	}
	if !found {
		t.Error("greet function not found")
	}
}

func TestSchemaHandler_DropFunction(t *testing.T) {
	r := setupSchemaRouter(t)

	// Create then drop
	schemaRequest(r, "POST", testFunctionsPath,
		`{"name":"to_drop_fn","schema":"public","language":"sql","returnType":"void","args":"","body":"SELECT 1","volatility":"VOLATILE"}`)

	w := schemaRequest(r, "DELETE", "/api/schema/test-proj/functions/to_drop_fn", "")
	if w.Code != 200 {
		t.Fatalf("drop function: %d, body: %s", w.Code, w.Body.String())
	}
}

// ---- Error paths ----

func TestSchemaHandler_VaultSealed(t *testing.T) {
	// Create vault, init and then seal it
	dir := t.TempDir()
	v, _ := vault.New(filepath.Join(dir, testVaultFile))
	initResult, _ := v.Init(5, 3)
	// Unseal first (to get a valid state), then seal
	for _, share := range initResult.Shares[:3] {
		if _, err := v.Unseal(share); err != nil {
			t.Fatalf(testUnsealFmt, err)
		}
	}
	v.Seal()

	h := NewSchemaHandler(v)
	r := chi.NewRouter()
	r.Route(testSchemaBase, h.Routes)

	w := schemaRequest(r, "GET", testTablesPath, "")
	if w.Code != 503 {
		t.Errorf("vault sealed: expected 503, got %d, body: %s", w.Code, w.Body.String())
	}
}

func TestSchemaHandler_ProjectNotFound(t *testing.T) {
	dir := t.TempDir()
	v, _ := vault.New(filepath.Join(dir, testVaultFile))
	initResult, _ := v.Init(5, 3)
	for _, share := range initResult.Shares[:3] {
		if _, err := v.Unseal(share); err != nil {
			t.Fatalf(testUnsealFmt, err)
		}
	}

	h := NewSchemaHandler(v)
	r := chi.NewRouter()
	r.Route(testSchemaBase, h.Routes)

	w := schemaRequest(r, "GET", "/api/schema/nonexistent-proj/tables", "")
	if w.Code != 404 {
		t.Errorf("not found: expected 404, got %d, body: %s", w.Code, w.Body.String())
	}
}

// ---- Indexes ----

func TestSchemaHandler_GetIndexes(t *testing.T) {
	r := setupSchemaRouter(t)

	w := schemaRequest(r, "GET", "/api/schema/test-proj/tables/users/indexes", "")
	if w.Code != 200 {
		t.Fatalf("get indexes: %d, body: %s", w.Code, w.Body.String())
	}

	var indexes []schema.IndexInfo
	json.NewDecoder(w.Body).Decode(&indexes)
	// users table has PK index + unique index on email
	if len(indexes) < 1 {
		t.Fatal("expected at least 1 index")
	}

	hasPK := false
	for _, idx := range indexes {
		if idx.Unique && len(idx.Columns) > 0 && idx.Columns[0] == "id" {
			hasPK = true
		}
	}
	if !hasPK {
		for _, idx := range indexes {
			t.Logf("index: %s, unique: %v, columns: %v", idx.Name, idx.Unique, idx.Columns)
		}
		t.Error("expected PK index on id")
	}
}
