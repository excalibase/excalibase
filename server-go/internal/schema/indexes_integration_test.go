//go:build integration

package schema

import (
	"context"
	"database/sql"
	"testing"
)

const indexCreateTableErrFmt = "create table: %v"

// setupIndexTable creates the common index test table structure.
func setupIndexTable(t *testing.T, cols []CreateColumnDef) (*Introspector, *sql.DB, context.Context, func()) {
	t.Helper()
	_, appDB, cleanup := setupPG(t)
	introspector := NewIntrospector()
	ctx := context.Background()
	if err := introspector.CreateTable(ctx, appDB, CreateTableRequest{
		Name: "index_test", Schema: "public",
		Columns: cols,
	}); err != nil {
		cleanup()
		t.Fatalf(indexCreateTableErrFmt, err)
	}
	return introspector, appDB, ctx, cleanup
}

// assertIndexType verifies that the named index has the expected type.
func assertIndexType(t *testing.T, i *Introspector, db *sql.DB, ctx context.Context, idxName, wantType string) {
	t.Helper()
	indexes, _ := i.GetIndexes(ctx, db, "public", "index_test")
	for _, idx := range indexes {
		if idx.Name == idxName && idx.Type != wantType {
			t.Errorf("index %s: expected type %s, got %s", idxName, wantType, idx.Type)
		}
	}
}

// assertIndexUnique fails when the named index is not unique.
func assertIndexUnique(t *testing.T, i *Introspector, db *sql.DB, ctx context.Context, name string) {
	t.Helper()
	indexes, _ := i.GetIndexes(ctx, db, "public", "index_test")
	for _, idx := range indexes {
		if idx.Name == name && !idx.Unique {
			t.Error("expected unique index")
		}
	}
}

// assertIndexColumnCount fails when the named index has fewer than wantCols columns.
func assertIndexColumnCount(t *testing.T, i *Introspector, db *sql.DB, ctx context.Context, name string, wantCols int) {
	t.Helper()
	indexes, _ := i.GetIndexes(ctx, db, "public", "index_test")
	for _, idx := range indexes {
		if idx.Name == name && len(idx.Columns) < wantCols {
			t.Errorf("expected %d columns, got %d", wantCols, len(idx.Columns))
		}
	}
}

func TestIntegration_Indexes_Create(t *testing.T) {
	introspector, appDB, ctx, cleanup := setupIndexTable(t, []CreateColumnDef{
		{Name: "id", Type: "serial", PrimaryKey: true},
		{Name: "email", Type: "text"},
		{Name: "name", Type: "text"},
		{Name: "tags", Type: "jsonb"},
	})
	defer cleanup()

	t.Run("btree index", func(t *testing.T) {
		if err := introspector.CreateIndex(ctx, appDB, CreateIndexRequest{
			Name:    "idx_email",
			Table:   "index_test",
			Schema:  "public",
			Columns: []string{"email"},
			Unique:  false,
			Type:    "btree",
		}); err != nil {
			t.Fatalf("CreateIndex btree: %v", err)
		}
		assertIndexProperties(t, introspector, appDB, ctx, "idx_email", false, "btree")
	})

	t.Run("unique index", func(t *testing.T) {
		if err := introspector.CreateIndex(ctx, appDB, CreateIndexRequest{
			Name:    "idx_email_unique",
			Table:   "index_test",
			Schema:  "public",
			Columns: []string{"email"},
			Unique:  true,
			Type:    "btree",
		}); err != nil {
			t.Fatalf("CreateIndex unique: %v", err)
		}
		assertIndexUnique(t, introspector, appDB, ctx, "idx_email_unique")
	})

	t.Run("multi-column index", func(t *testing.T) {
		if err := introspector.CreateIndex(ctx, appDB, CreateIndexRequest{
			Name:    "idx_email_name",
			Table:   "index_test",
			Schema:  "public",
			Columns: []string{"email", "name"},
			Type:    "btree",
		}); err != nil {
			t.Fatalf("CreateIndex multi-column: %v", err)
		}
		assertIndexColumnCount(t, introspector, appDB, ctx, "idx_email_name", 2)
	})
}

// assertIndexProperties finds an index by name and checks uniqueness and type.
func assertIndexProperties(t *testing.T, i *Introspector, db *sql.DB, ctx context.Context, name string, wantUnique bool, wantType string) {
	t.Helper()
	indexes, err := i.GetIndexes(ctx, db, "public", "index_test")
	if err != nil {
		t.Fatalf("GetIndexes: %v", err)
	}
	for _, idx := range indexes {
		if idx.Name != name {
			continue
		}
		if idx.Unique != wantUnique {
			t.Errorf("index %s: expected unique=%v, got %v", name, wantUnique, idx.Unique)
		}
		if idx.Type != wantType {
			t.Errorf("index %s: expected type %s, got %s", name, wantType, idx.Type)
		}
		return
	}
	t.Errorf("index %s not found", name)
}

func TestIntegration_Indexes_Types(t *testing.T) {
	introspector, appDB, ctx, cleanup := setupIndexTable(t, []CreateColumnDef{
		{Name: "id", Type: "serial", PrimaryKey: true},
		{Name: "name", Type: "text"},
		{Name: "tags", Type: "jsonb"},
	})
	defer cleanup()

	t.Run("hash index", func(t *testing.T) {
		if err := introspector.CreateIndex(ctx, appDB, CreateIndexRequest{
			Name:    "idx_name_hash",
			Table:   "index_test",
			Schema:  "public",
			Columns: []string{"name"},
			Type:    "hash",
		}); err != nil {
			t.Fatalf("CreateIndex hash: %v", err)
		}
		assertIndexType(t, introspector, appDB, ctx, "idx_name_hash", "hash")
	})

	t.Run("gin index", func(t *testing.T) {
		if err := introspector.CreateIndex(ctx, appDB, CreateIndexRequest{
			Name:    "idx_tags_gin",
			Table:   "index_test",
			Schema:  "public",
			Columns: []string{"tags"},
			Type:    "gin",
		}); err != nil {
			t.Fatalf("CreateIndex gin: %v", err)
		}
		assertIndexType(t, introspector, appDB, ctx, "idx_tags_gin", "gin")
	})

	t.Run("invalid index type rejected", func(t *testing.T) {
		err := introspector.CreateIndex(ctx, appDB, CreateIndexRequest{
			Name: "idx_bad", Table: "index_test", Schema: "public",
			Columns: []string{"name"}, Type: "invalid_type",
		})
		if err == nil {
			t.Fatal("expected error for invalid index type")
		}
	})

	t.Run("empty columns rejected", func(t *testing.T) {
		err := introspector.CreateIndex(ctx, appDB, CreateIndexRequest{
			Name: "idx_nocols", Table: "index_test", Schema: "public",
			Columns: []string{}, Type: "btree",
		})
		if err == nil {
			t.Fatal("expected error for empty columns")
		}
	})
}

func TestIntegration_Indexes_Drop(t *testing.T) {
	introspector, appDB, ctx, cleanup := setupIndexTable(t, []CreateColumnDef{
		{Name: "id", Type: "serial", PrimaryKey: true},
		{Name: "email", Type: "text"},
	})
	defer cleanup()

	if err := introspector.CreateIndex(ctx, appDB, CreateIndexRequest{
		Name: "idx_drop_me", Table: "index_test", Schema: "public",
		Columns: []string{"email"}, Type: "btree",
	}); err != nil {
		t.Fatalf("setup index: %v", err)
	}

	t.Run("drop index", func(t *testing.T) {
		if err := introspector.DropIndex(ctx, appDB, "public", "idx_drop_me"); err != nil {
			t.Fatalf("DropIndex: %v", err)
		}
		indexes, _ := introspector.GetIndexes(ctx, appDB, "public", "index_test")
		for _, idx := range indexes {
			if idx.Name == "idx_drop_me" {
				t.Error("index should be dropped")
			}
		}
	})
}

func TestIntegration_Types_Enum(t *testing.T) {
	_, appDB, cleanup := setupPG(t)
	defer cleanup()
	introspector := NewIntrospector()
	ctx := context.Background()

	if _, err := appDB.ExecContext(ctx, "CREATE TYPE mood AS ENUM ('sad', 'ok', 'happy')"); err != nil {
		t.Fatalf("create enum type: %v", err)
	}
	if _, err := appDB.ExecContext(ctx, "CREATE TYPE address AS (street text, city text, zip text)"); err != nil {
		t.Fatalf("create composite type: %v", err)
	}
	if _, err := appDB.ExecContext(ctx, "CREATE DOMAIN positive_int AS integer CHECK (VALUE > 0)"); err != nil {
		t.Fatalf("create domain type: %v", err)
	}

	t.Run("get types returns expected kinds", func(t *testing.T) {
		types, err := introspector.GetTypes(ctx, appDB, "public")
		if err != nil {
			t.Fatalf("GetTypes: %v", err)
		}
		assertTypesPresent(t, types)
	})

	t.Run("enum values are ordered", func(t *testing.T) {
		types, _ := introspector.GetTypes(ctx, appDB, "public")
		for _, pt := range types {
			if pt.Name == "mood" {
				if pt.Values[0] != "sad" || pt.Values[1] != "ok" || pt.Values[2] != "happy" {
					t.Errorf("enum values not in order: %v", pt.Values)
				}
			}
		}
	})
}

// assertEnumType validates the mood enum: kind, value count, and value set.
func assertEnumType(t *testing.T, pt PgTypeInfo) {
	t.Helper()
	if pt.Type != "enum" {
		t.Errorf("expected type enum, got %s", pt.Type)
	}
	if len(pt.Values) != 3 {
		t.Errorf("expected 3 enum values, got %d: %v", len(pt.Values), pt.Values)
	}
	expectedVals := map[string]bool{"sad": true, "ok": true, "happy": true}
	for _, v := range pt.Values {
		if !expectedVals[v] {
			t.Errorf("unexpected enum value: %s", v)
		}
	}
}

// assertTypeKind asserts a single PgTypeInfo matches the expected pg type kind.
func assertTypeKind(t *testing.T, pt PgTypeInfo, want string) {
	t.Helper()
	if pt.Type != want {
		t.Errorf("expected type %s, got %s", want, pt.Type)
	}
}

// assertTypesPresent validates that mood (enum), address (composite), and positive_int (domain) exist.
func assertTypesPresent(t *testing.T, types []PgTypeInfo) {
	t.Helper()
	foundEnum, foundComposite, foundDomain := false, false, false
	for _, pt := range types {
		switch pt.Name {
		case "mood":
			foundEnum = true
			assertEnumType(t, pt)
		case "address":
			foundComposite = true
			assertTypeKind(t, pt, "composite")
			if len(pt.Values) != 0 {
				t.Errorf("composite type should have empty values, got %v", pt.Values)
			}
		case "positive_int":
			foundDomain = true
			assertTypeKind(t, pt, "domain")
		}
	}
	if !foundEnum {
		t.Error("enum type 'mood' not found")
	}
	if !foundComposite {
		t.Error("composite type 'address' not found")
	}
	if !foundDomain {
		t.Error("domain type 'positive_int' not found")
	}
}
