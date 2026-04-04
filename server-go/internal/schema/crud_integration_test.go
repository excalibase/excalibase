//go:build integration

package schema

import (
	"context"
	"testing"
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

		tables, _ := introspector.GetTables(ctx, appDB, "public")
		found := false
		for _, tbl := range tables {
			if tbl.Name == "test_create" {
				found = true
			}
		}
		if !found {
			t.Error("table test_create not found after creation")
		}

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

func TestIntegration_UpdateTable(t *testing.T) {
	_, appDB, cleanup := setupPG(t)
	defer cleanup()

	introspector := NewIntrospector()
	ctx := context.Background()

	// Create table first
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

		tables, _ := introspector.GetTables(ctx, appDB, "public")
		found := false
		for _, tbl := range tables {
			if tbl.Name == "test_renamed" {
				found = true
			}
		}
		if !found {
			t.Error("renamed table not found")
		}
	})

	t.Run("enable RLS", func(t *testing.T) {
		enabled := true
		if err := introspector.UpdateTable(ctx, appDB, "public", "test_renamed", UpdateTableRequest{RlsEnabled: &enabled}); err != nil {
			t.Fatalf("enable RLS: %v", err)
		}
		// Verify RLS is enabled by checking pg_class
		var rlsEnabled bool
		err := appDB.QueryRowContext(ctx,
			"SELECT relrowsecurity FROM pg_class WHERE relname = 'test_renamed'").Scan(&rlsEnabled)
		if err != nil {
			t.Fatalf("check RLS: %v", err)
		}
		if !rlsEnabled {
			t.Error("RLS should be enabled")
		}
	})

	t.Run("set comment", func(t *testing.T) {
		comment := "updated comment"
		if err := introspector.UpdateTable(ctx, appDB, "public", "test_renamed", UpdateTableRequest{Comment: &comment}); err != nil {
			t.Fatalf("set comment: %v", err)
		}
	})
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

		tables, _ := introspector.GetTables(ctx, appDB, "public")
		for _, tbl := range tables {
			if tbl.Name == "test_drop" {
				t.Error("table should be dropped")
			}
		}
	})

	t.Run("drop with cascade", func(t *testing.T) {
		// Create parent and child tables
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

		cols, _ := introspector.GetColumns(ctx, appDB, "public", "test_cols")
		found := false
		for _, c := range cols {
			if c.Name == "description" {
				found = true
				if !c.Nullable {
					t.Error("column should be nullable")
				}
			}
		}
		if !found {
			t.Error("column not found")
		}
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

		cols, _ := introspector.GetColumns(ctx, appDB, "public", "test_alter")
		found := false
		for _, c := range cols {
			if c.Name == "full_name" {
				found = true
			}
		}
		if !found {
			t.Error("renamed column not found")
		}
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
		for _, r := range roles {
			if r.Name == "pg_signal_backend" || r.Name == "pg_read_all_stats" {
				t.Errorf("system role %s should be excluded", r.Name)
			}
		}
		// should have superuser and excalibase_app
		names := map[string]bool{}
		for _, r := range roles {
			names[r.Name] = true
		}
		if !names["superuser"] {
			t.Error("expected 'superuser' role")
		}
		if !names["excalibase_app"] {
			t.Error("expected 'excalibase_app' role")
		}
	})

	t.Run("create and drop role", func(t *testing.T) {
		pwd := "testpass123"
		if err := introspector.CreateRole(ctx, superDB, CreateRoleRequest{
			Name: "test_role", Password: &pwd, Login: true,
		}); err != nil {
			t.Fatalf("CreateRole: %v", err)
		}

		roles, _ := introspector.GetRoles(ctx, superDB)
		found := false
		for _, r := range roles {
			if r.Name == "test_role" {
				found = true
				if !r.Login {
					t.Error("role should have login")
				}
			}
		}
		if !found {
			t.Error("created role not found")
		}

		if err := introspector.DropRole(ctx, superDB, "test_role"); err != nil {
			t.Fatalf("DropRole: %v", err)
		}

		roles, _ = introspector.GetRoles(ctx, superDB)
		for _, r := range roles {
			if r.Name == "test_role" {
				t.Error("role should be dropped")
			}
		}
	})
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
		// plpgsql is installed by default
		found := false
		for _, e := range exts {
			if e.Name == "plpgsql" {
				found = true
				if e.InstalledVersion == nil {
					t.Error("plpgsql should have installed version")
				}
			}
		}
		if !found {
			t.Error("plpgsql not found")
		}
	})

	t.Run("create and drop extension", func(t *testing.T) {
		if err := introspector.CreateExtension(ctx, superDB, "pg_trgm", ""); err != nil {
			t.Fatalf("CreateExtension: %v", err)
		}

		exts, _ := introspector.GetExtensions(ctx, superDB)
		found := false
		for _, e := range exts {
			if e.Name == "pg_trgm" && e.InstalledVersion != nil {
				found = true
			}
		}
		if !found {
			t.Error("pg_trgm not installed")
		}

		if err := introspector.DropExtension(ctx, superDB, "pg_trgm", false); err != nil {
			t.Fatalf("DropExtension: %v", err)
		}
	})
}

// --- Policies ---

func TestIntegration_Policies(t *testing.T) {
	_, appDB, cleanup := setupPG(t)
	defer cleanup()

	introspector := NewIntrospector()
	ctx := context.Background()

	// Create a table owned by appDB user, then enable RLS
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

		policies, err := introspector.GetPolicies(ctx, appDB, "public")
		if err != nil {
			t.Fatalf("GetPolicies: %v", err)
		}
		found := false
		for _, p := range policies {
			if p.Name == "policy_test_select" {
				found = true
				if p.Command != "SELECT" {
					t.Errorf("expected command SELECT, got %s", p.Command)
				}
				if !p.Permissive {
					t.Error("policy should be permissive")
				}
			}
		}
		if !found {
			t.Error("policy not found")
		}
	})

	t.Run("drop policy", func(t *testing.T) {
		if err := introspector.DropPolicy(ctx, appDB, "policy_test", "policy_test_select"); err != nil {
			t.Fatalf("DropPolicy: %v", err)
		}

		policies, _ := introspector.GetPolicies(ctx, appDB, "public")
		for _, p := range policies {
			if p.Name == "policy_test_select" {
				t.Error("policy should be dropped")
			}
		}
	})
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

		funcs, err := introspector.GetFunctions(ctx, appDB, "public")
		if err != nil {
			t.Fatalf("GetFunctions: %v", err)
		}
		found := false
		for _, f := range funcs {
			if f.Name == "add_numbers" {
				found = true
				if f.Language != "sql" {
					t.Errorf("expected language sql, got %s", f.Language)
				}
				if f.Volatility != "IMMUTABLE" {
					t.Errorf("expected IMMUTABLE, got %s", f.Volatility)
				}
			}
		}
		if !found {
			t.Error("function not found")
		}
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

		funcs, _ := introspector.GetFunctions(ctx, appDB, "public")
		for _, f := range funcs {
			if f.Name == "add_numbers" {
				t.Error("function should be dropped")
			}
		}
	})
}
