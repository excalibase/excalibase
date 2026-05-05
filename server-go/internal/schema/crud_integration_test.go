//go:build integration

package schema

import (
	"context"
	"database/sql"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/testutil"
)

// --- Tables CRUD ---

func TestIntegration_CreateTable(t *testing.T) {
	superDB, appDB, cleanup := setupPG(t)
	_ = superDB
	defer cleanup()

	introspector := NewIntrospector()
	ctx := context.Background()

	t.Run("basic table with columns", func(t *testing.T) {
		defVal := "now()"
		req := CreateTableRequest{
			Name:   "test_create",
			Schema: "public",
			Columns: []CreateColumnDef{
				{Name: "id", Type: "serial", PrimaryKey: true},
				{Name: "name", Type: "text", Nullable: false},
				{Name: "email", Type: "varchar(255)", Nullable: true, Unique: true},
				{Name: "created_at", Type: "timestamptz", Default: &defVal},
			},
		}
		if err := introspector.CreateTable(ctx, appDB, req); err != nil {
			t.Fatalf("CreateTable: %v", err)
		}

		assertTableExists(t, introspector, appDB, ctx, "test_create")

		cols, _ := introspector.GetColumns(ctx, appDB, "public", "test_create")
		if len(cols) != 4 {
			t.Fatalf("expected 4 columns, got %d", len(cols))
		}
	})

	t.Run("table with comment", func(t *testing.T) {
		req := CreateTableRequest{
			Name:    "test_comment",
			Schema:  "public",
			Comment: "This is a test table",
			Columns: []CreateColumnDef{
				{Name: "id", Type: "serial", PrimaryKey: true},
			},
		}
		if err := introspector.CreateTable(ctx, appDB, req); err != nil {
			t.Fatalf("CreateTable with comment: %v", err)
		}
	})

	t.Run("empty table no columns", func(t *testing.T) {
		req := CreateTableRequest{
			Name:   "test_empty",
			Schema: "public",
		}
		if err := introspector.CreateTable(ctx, appDB, req); err != nil {
			t.Fatalf("CreateTable empty: %v", err)
		}
	})
}

// assertTableExists checks that a table with the given name exists in public schema.
func assertTableExists(t *testing.T, i *Introspector, db *sql.DB, ctx context.Context, name string) {
	t.Helper()
	tables, _ := i.GetTables(ctx, db, "public")
	for _, tbl := range tables {
		if tbl.Name == name {
			return
		}
	}
	t.Errorf("table %s not found after creation", name)
}

// assertTableAbsent checks that a table with the given name does not exist in public schema.
func assertTableAbsent(t *testing.T, i *Introspector, db *sql.DB, ctx context.Context, name string) {
	t.Helper()
	tables, _ := i.GetTables(ctx, db, "public")
	for _, tbl := range tables {
		if tbl.Name == name {
			t.Errorf("table %s should not exist", name)
		}
	}
}

func TestIntegration_UpdateTable(t *testing.T) {
	_, appDB, cleanup := setupPG(t)
	defer cleanup()

	introspector := NewIntrospector()
	ctx := context.Background()

	req := CreateTableRequest{
		Name:   "test_update",
		Schema: "public",
		Columns: []CreateColumnDef{
			{Name: "id", Type: "serial", PrimaryKey: true},
		},
	}
	if err := introspector.CreateTable(ctx, appDB, req); err != nil {
		t.Fatalf("setup: %v", err)
	}

	t.Run("rename table", func(t *testing.T) {
		newName := "test_renamed"
		if err := introspector.UpdateTable(ctx, appDB, "public", "test_update", UpdateTableRequest{NewName: &newName}); err != nil {
			t.Fatalf("rename: %v", err)
		}
		assertTableExists(t, introspector, appDB, ctx, "test_renamed")
	})

	t.Run("enable RLS", func(t *testing.T) {
		enabled := true
		if err := introspector.UpdateTable(ctx, appDB, "public", "test_renamed", UpdateTableRequest{RlsEnabled: &enabled}); err != nil {
			t.Fatalf("enable RLS: %v", err)
		}
		assertRLSEnabled(t, appDB, ctx, "test_renamed")
	})

	t.Run("set comment", func(t *testing.T) {
		comment := "updated comment"
		if err := introspector.UpdateTable(ctx, appDB, "public", "test_renamed", UpdateTableRequest{Comment: &comment}); err != nil {
			t.Fatalf("set comment: %v", err)
		}
	})
}

// assertRLSEnabled checks that row-level security is enabled on a table.
func assertRLSEnabled(t *testing.T, db *sql.DB, ctx context.Context, tableName string) {
	t.Helper()
	var rlsEnabled bool
	err := db.QueryRowContext(ctx,
		"SELECT relrowsecurity FROM pg_class WHERE relname = $1", tableName).Scan(&rlsEnabled)
	if err != nil {
		t.Fatalf("check RLS: %v", err)
	}
	if !rlsEnabled {
		t.Error("RLS should be enabled")
	}
}

func TestIntegration_DropTable(t *testing.T) {
	_, appDB, cleanup := setupPG(t)
	defer cleanup()

	introspector := NewIntrospector()
	ctx := context.Background()

	req := CreateTableRequest{
		Name:   "test_drop",
		Schema: "public",
		Columns: []CreateColumnDef{
			{Name: "id", Type: "serial", PrimaryKey: true},
		},
	}
	if err := introspector.CreateTable(ctx, appDB, req); err != nil {
		t.Fatalf("setup: %v", err)
	}

	t.Run("drop existing table", func(t *testing.T) {
		if err := introspector.DropTable(ctx, appDB, "public", "test_drop", false); err != nil {
			t.Fatalf("DropTable: %v", err)
		}
		assertTableAbsent(t, introspector, appDB, ctx, "test_drop")
	})

	t.Run("drop with cascade", func(t *testing.T) {
		parent := CreateTableRequest{
			Name: "parent_tbl", Schema: "public",
			Columns: []CreateColumnDef{{Name: "id", Type: "serial", PrimaryKey: true}},
		}
		introspector.CreateTable(ctx, appDB, parent)
		appDB.ExecContext(ctx, `CREATE TABLE child_tbl (id serial PRIMARY KEY, parent_id int REFERENCES parent_tbl(id))`)

		if err := introspector.DropTable(ctx, appDB, "public", "parent_tbl", true); err != nil {
			t.Fatalf("DropTable cascade: %v", err)
		}
	})
}

// --- Columns CRUD ---

func TestIntegration_AddColumn(t *testing.T) {
	_, appDB, cleanup := setupPG(t)
	defer cleanup()

	introspector := NewIntrospector()
	ctx := context.Background()

	introspector.CreateTable(ctx, appDB, CreateTableRequest{
		Name: "test_cols", Schema: "public",
		Columns: []CreateColumnDef{{Name: "id", Type: "serial", PrimaryKey: true}},
	})

	t.Run("add nullable column", func(t *testing.T) {
		if err := introspector.AddColumn(ctx, appDB, "public", "test_cols", AddColumnRequest{
			Name: "description", Type: "text", Nullable: true,
		}); err != nil {
			t.Fatalf("AddColumn: %v", err)
		}
		assertColumnNullable(t, introspector, appDB, ctx, "test_cols", "description", true)
	})

	t.Run("add column with default", func(t *testing.T) {
		def := "'active'"
		if err := introspector.AddColumn(ctx, appDB, "public", "test_cols", AddColumnRequest{
			Name: "status", Type: "text", Nullable: false, Default: &def,
		}); err != nil {
			t.Fatalf("AddColumn with default: %v", err)
		}
	})

	t.Run("add unique column", func(t *testing.T) {
		if err := introspector.AddColumn(ctx, appDB, "public", "test_cols", AddColumnRequest{
			Name: "code", Type: "varchar(50)", Unique: true, Nullable: true,
		}); err != nil {
			t.Fatalf("AddColumn unique: %v", err)
		}
	})
}

// assertColumnNullable checks that the named column has the expected nullable setting.
func assertColumnNullable(t *testing.T, i *Introspector, db *sql.DB, ctx context.Context, table, colName string, wantNullable bool) {
	t.Helper()
	cols, _ := i.GetColumns(ctx, db, "public", table)
	for _, c := range cols {
		if c.Name != colName {
			continue
		}
		if c.Nullable != wantNullable {
			t.Errorf("column %s: expected nullable=%v, got %v", colName, wantNullable, c.Nullable)
		}
		return
	}
	t.Errorf("column %s not found", colName)
}

func TestIntegration_AlterColumn(t *testing.T) {
	_, appDB, cleanup := setupPG(t)
	defer cleanup()

	introspector := NewIntrospector()
	ctx := context.Background()

	introspector.CreateTable(ctx, appDB, CreateTableRequest{
		Name: "test_alter", Schema: "public",
		Columns: []CreateColumnDef{
			{Name: "id", Type: "serial", PrimaryKey: true},
			{Name: "name", Type: "varchar(100)", Nullable: true},
		},
	})

	t.Run("rename column", func(t *testing.T) {
		newName := "full_name"
		if err := introspector.AlterColumn(ctx, appDB, "public", "test_alter", "name", AlterColumnRequest{
			NewName: &newName,
		}); err != nil {
			t.Fatalf("rename column: %v", err)
		}
		assertColumnExists(t, introspector, appDB, ctx, "test_alter", "full_name")
	})

	t.Run("change type", func(t *testing.T) {
		newType := "text"
		if err := introspector.AlterColumn(ctx, appDB, "public", "test_alter", "full_name", AlterColumnRequest{
			Type: &newType,
		}); err != nil {
			t.Fatalf("change type: %v", err)
		}
	})

	t.Run("set not null", func(t *testing.T) {
		notNull := false
		if err := introspector.AlterColumn(ctx, appDB, "public", "test_alter", "full_name", AlterColumnRequest{
			Nullable: &notNull,
		}); err != nil {
			t.Fatalf("set not null: %v", err)
		}
	})

	t.Run("set default", func(t *testing.T) {
		def := "'unknown'"
		if err := introspector.AlterColumn(ctx, appDB, "public", "test_alter", "full_name", AlterColumnRequest{
			Default: &def,
		}); err != nil {
			t.Fatalf("set default: %v", err)
		}
	})

	t.Run("drop default", func(t *testing.T) {
		if err := introspector.AlterColumn(ctx, appDB, "public", "test_alter", "full_name", AlterColumnRequest{
			DropDefault: true,
		}); err != nil {
			t.Fatalf("drop default: %v", err)
		}
	})
}

// assertColumnExists checks that the named column is present in the given table.
func assertColumnExists(t *testing.T, i *Introspector, db *sql.DB, ctx context.Context, table, colName string) {
	t.Helper()
	cols, _ := i.GetColumns(ctx, db, "public", table)
	for _, c := range cols {
		if c.Name == colName {
			return
		}
	}
	t.Errorf("column %s not found in table %s", colName, table)
}

func TestIntegration_DropColumn(t *testing.T) {
	_, appDB, cleanup := setupPG(t)
	defer cleanup()

	introspector := NewIntrospector()
	ctx := context.Background()

	introspector.CreateTable(ctx, appDB, CreateTableRequest{
		Name: "test_dropcol", Schema: "public",
		Columns: []CreateColumnDef{
			{Name: "id", Type: "serial", PrimaryKey: true},
			{Name: "name", Type: "text"},
			{Name: "extra", Type: "text"},
		},
	})

	if err := introspector.DropColumn(ctx, appDB, "public", "test_dropcol", "extra"); err != nil {
		t.Fatalf("DropColumn: %v", err)
	}

	cols, _ := introspector.GetColumns(ctx, appDB, "public", "test_dropcol")
	for _, c := range cols {
		if c.Name == "extra" {
			t.Error("column 'extra' should be dropped")
		}
	}
}

// --- Roles ---

func TestIntegration_Roles(t *testing.T) {
	superDB, _, cleanup := setupPG(t)
	defer cleanup()

	introspector := NewIntrospector()
	ctx := context.Background()

	t.Run("get roles excludes system", func(t *testing.T) {
		roles, err := introspector.GetRoles(ctx, superDB)
		if err != nil {
			t.Fatalf("GetRoles: %v", err)
		}
		assertSystemRolesExcluded(t, roles)
		assertExpectedRolesPresent(t, roles)
	})

	t.Run("create and drop role", func(t *testing.T) {
		pwd := testutil.FixturePassword("schema-role")
		if err := introspector.CreateRole(ctx, superDB, CreateRoleRequest{
			Name: "test_role", Password: &pwd, Login: true,
		}); err != nil {
			t.Fatalf("CreateRole: %v", err)
		}
		assertRoleLogin(t, introspector, superDB, ctx, "test_role", true)

		if err := introspector.DropRole(ctx, superDB, "test_role"); err != nil {
			t.Fatalf("DropRole: %v", err)
		}
		assertRoleAbsent(t, introspector, superDB, ctx, "test_role")
	})
}

// assertSystemRolesExcluded verifies known system roles are absent from the list.
func assertSystemRolesExcluded(t *testing.T, roles []RoleInfo) {
	t.Helper()
	for _, r := range roles {
		if r.Name == "pg_signal_backend" || r.Name == "pg_read_all_stats" {
			t.Errorf("system role %s should be excluded", r.Name)
		}
	}
}

// assertExpectedRolesPresent verifies that the fixture roles exist.
func assertExpectedRolesPresent(t *testing.T, roles []RoleInfo) {
	t.Helper()
	names := make(map[string]bool, len(roles))
	for _, r := range roles {
		names[r.Name] = true
	}
	if !names["superuser"] {
		t.Error("expected 'superuser' role")
	}
	if !names["excalibase_app"] {
		t.Error("expected 'excalibase_app' role")
	}
}

// assertRoleLogin checks that the named role has the expected login capability.
func assertRoleLogin(t *testing.T, i *Introspector, db *sql.DB, ctx context.Context, name string, wantLogin bool) {
	t.Helper()
	roles, _ := i.GetRoles(ctx, db)
	for _, r := range roles {
		if r.Name != name {
			continue
		}
		if r.Login != wantLogin {
			t.Errorf("role %s: expected login=%v, got %v", name, wantLogin, r.Login)
		}
		return
	}
	t.Errorf("role %s not found", name)
}

// assertRoleAbsent verifies a role no longer exists.
func assertRoleAbsent(t *testing.T, i *Introspector, db *sql.DB, ctx context.Context, name string) {
	t.Helper()
	roles, _ := i.GetRoles(ctx, db)
	for _, r := range roles {
		if r.Name == name {
			t.Errorf("role %s should be dropped", name)
		}
	}
}

// --- Extensions ---

func TestIntegration_Extensions(t *testing.T) {
	superDB, _, cleanup := setupPG(t)
	defer cleanup()

	introspector := NewIntrospector()
	ctx := context.Background()

	t.Run("get extensions", func(t *testing.T) {
		exts, err := introspector.GetExtensions(ctx, superDB)
		if err != nil {
			t.Fatalf("GetExtensions: %v", err)
		}
		assertExtensionInstalled(t, exts, "plpgsql")
	})

	t.Run("create and drop extension", func(t *testing.T) {
		if err := introspector.CreateExtension(ctx, superDB, "pg_trgm", ""); err != nil {
			t.Fatalf("CreateExtension: %v", err)
		}
		assertExtensionInstalled(t, mustGetExtensions(t, introspector, superDB, ctx), "pg_trgm")

		if err := introspector.DropExtension(ctx, superDB, "pg_trgm", false); err != nil {
			t.Fatalf("DropExtension: %v", err)
		}
	})
}

// assertExtensionInstalled checks that the named extension is installed.
func assertExtensionInstalled(t *testing.T, exts []ExtensionInfo, name string) {
	t.Helper()
	for _, e := range exts {
		if e.Name == name && e.InstalledVersion != nil {
			return
		}
	}
	t.Errorf("extension %s not found or not installed", name)
}

// mustGetExtensions returns extensions or fails the test.
func mustGetExtensions(t *testing.T, i *Introspector, db *sql.DB, ctx context.Context) []ExtensionInfo {
	t.Helper()
	exts, _ := i.GetExtensions(ctx, db)
	return exts
}

// --- Policies ---

func TestIntegration_Policies(t *testing.T) {
	_, appDB, cleanup := setupPG(t)
	defer cleanup()

	introspector := NewIntrospector()
	ctx := context.Background()

	introspector.CreateTable(ctx, appDB, CreateTableRequest{
		Name: "policy_test", Schema: "public",
		Columns: []CreateColumnDef{
			{Name: "id", Type: "serial", PrimaryKey: true},
			{Name: "owner", Type: "text"},
		},
	})
	appDB.ExecContext(ctx, "ALTER TABLE policy_test ENABLE ROW LEVEL SECURITY")

	t.Run("create and get policy", func(t *testing.T) {
		usingExpr := "true"
		if err := introspector.CreatePolicy(ctx, appDB, CreatePolicyRequest{
			Name:       "policy_test_select",
			Table:      "policy_test",
			Schema:     "public",
			Command:    "SELECT",
			Roles:      "public",
			Using:      &usingExpr,
			Permissive: true,
		}); err != nil {
			t.Fatalf("CreatePolicy: %v", err)
		}
		assertPolicyPresent(t, introspector, appDB, ctx, "policy_test_select", "SELECT", true)
	})

	t.Run("drop policy", func(t *testing.T) {
		if err := introspector.DropPolicy(ctx, appDB, "policy_test", "policy_test_select"); err != nil {
			t.Fatalf("DropPolicy: %v", err)
		}
		assertPolicyAbsent(t, introspector, appDB, ctx, "policy_test_select")
	})
}

// assertPolicyPresent checks that a policy with the given name, command, and permissive setting exists.
func assertPolicyPresent(t *testing.T, i *Introspector, db *sql.DB, ctx context.Context, name, command string, permissive bool) {
	t.Helper()
	policies, err := i.GetPolicies(ctx, db, "public")
	if err != nil {
		t.Fatalf("GetPolicies: %v", err)
	}
	for _, p := range policies {
		if p.Name != name {
			continue
		}
		if p.Command != command {
			t.Errorf("policy %s: expected command %s, got %s", name, command, p.Command)
		}
		if p.Permissive != permissive {
			t.Errorf("policy %s: expected permissive=%v, got %v", name, permissive, p.Permissive)
		}
		return
	}
	t.Errorf("policy %s not found", name)
}

// assertPolicyAbsent verifies that a policy no longer exists.
func assertPolicyAbsent(t *testing.T, i *Introspector, db *sql.DB, ctx context.Context, name string) {
	t.Helper()
	policies, _ := i.GetPolicies(ctx, db, "public")
	for _, p := range policies {
		if p.Name == name {
			t.Errorf("policy %s should be dropped", name)
		}
	}
}

// --- Functions ---

func TestIntegration_Functions(t *testing.T) {
	_, appDB, cleanup := setupPG(t)
	defer cleanup()

	introspector := NewIntrospector()
	ctx := context.Background()

	t.Run("create and get function", func(t *testing.T) {
		if err := introspector.CreateFunction(ctx, appDB, CreateFunctionRequest{
			Name:       "add_numbers",
			Schema:     "public",
			Language:   "sql",
			ReturnType: "integer",
			Args:       "a integer, b integer",
			Body:       "SELECT a + b;",
			Volatility: "IMMUTABLE",
		}); err != nil {
			t.Fatalf("CreateFunction: %v", err)
		}
		assertFunctionProperties(t, introspector, appDB, ctx, "add_numbers", "sql", "IMMUTABLE")
	})

	t.Run("create plpgsql function", func(t *testing.T) {
		if err := introspector.CreateFunction(ctx, appDB, CreateFunctionRequest{
			Name:       "greet",
			Schema:     "public",
			Language:   "plpgsql",
			ReturnType: "text",
			Args:       "name text",
			Body:       "BEGIN RETURN 'Hello, ' || name; END;",
			Volatility: "STABLE",
		}); err != nil {
			t.Fatalf("CreateFunction plpgsql: %v", err)
		}
	})

	t.Run("drop function", func(t *testing.T) {
		if err := introspector.DropFunction(ctx, appDB, "public", "add_numbers", "integer, integer"); err != nil {
			t.Fatalf("DropFunction: %v", err)
		}
		assertFunctionAbsent(t, introspector, appDB, ctx, "add_numbers")
	})
}

// assertFunctionProperties checks language and volatility for a named function.
func assertFunctionProperties(t *testing.T, i *Introspector, db *sql.DB, ctx context.Context, name, language, volatility string) {
	t.Helper()
	funcs, err := i.GetFunctions(ctx, db, "public")
	if err != nil {
		t.Fatalf("GetFunctions: %v", err)
	}
	for _, f := range funcs {
		if f.Name != name {
			continue
		}
		if f.Language != language {
			t.Errorf("function %s: expected language %s, got %s", name, language, f.Language)
		}
		if f.Volatility != volatility {
			t.Errorf("function %s: expected volatility %s, got %s", name, volatility, f.Volatility)
		}
		return
	}
	t.Errorf("function %s not found", name)
}

// assertFunctionAbsent verifies that a function no longer exists.
func assertFunctionAbsent(t *testing.T, i *Introspector, db *sql.DB, ctx context.Context, name string) {
	t.Helper()
	funcs, _ := i.GetFunctions(ctx, db, "public")
	for _, f := range funcs {
		if f.Name == name {
			t.Errorf("function %s should be dropped", name)
		}
	}
}
