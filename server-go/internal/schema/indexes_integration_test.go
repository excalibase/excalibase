//go:build integration

package schema

import (
	"context"
	"testing"
)

func TestIntegration_Indexes(t *testing.T) {
	_, appDB, cleanup := setupPG(t)
	defer cleanup()

	introspector := NewIntrospector()
	ctx := context.Background()

	// Create a table to add indexes on
	if err := introspector.CreateTable(ctx, appDB, CreateTableRequest{
		Name: "index_test", Schema: "public",
		Columns: []CreateColumnDef{
			{Name: "id", Type: "serial", PrimaryKey: true},
			{Name: "email", Type: "text"},
			{Name: "name", Type: "text"},
			{Name: "tags", Type: "jsonb"},
		},
	}); err != nil {
		t.Fatalf("create table: %v", err)
	}

	t.Run("create btree index", func(t *testing.T) {
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

		indexes, err := introspector.GetIndexes(ctx, appDB, "public", "index_test")
		if err != nil {
			t.Fatalf("GetIndexes: %v", err)
		}
		found := false
		for _, idx := range indexes {
			if idx.Name == "idx_email" {
				found = true
				if idx.Unique {
					t.Error("expected non-unique index")
				}
				if idx.Type != "btree" {
					t.Errorf("expected btree, got %s", idx.Type)
				}
			}
		}
		if !found {
			t.Error("idx_email not found")
		}
	})

	t.Run("create unique index", func(t *testing.T) {
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

		indexes, _ := introspector.GetIndexes(ctx, appDB, "public", "index_test")
		for _, idx := range indexes {
			if idx.Name == "idx_email_unique" {
				if !idx.Unique {
					t.Error("expected unique index")
				}
			}
		}
	})

	t.Run("create multi-column index", func(t *testing.T) {
		if err := introspector.CreateIndex(ctx, appDB, CreateIndexRequest{
			Name:    "idx_email_name",
			Table:   "index_test",
			Schema:  "public",
			Columns: []string{"email", "name"},
			Unique:  false,
			Type:    "btree",
		}); err != nil {
			t.Fatalf("CreateIndex multi-column: %v", err)
		}

		indexes, _ := introspector.GetIndexes(ctx, appDB, "public", "index_test")
		for _, idx := range indexes {
			if idx.Name == "idx_email_name" {
				if len(idx.Columns) < 2 {
					t.Errorf("expected 2 columns, got %d", len(idx.Columns))
				}
			}
		}
	})

	t.Run("create hash index", func(t *testing.T) {
		if err := introspector.CreateIndex(ctx, appDB, CreateIndexRequest{
			Name:    "idx_name_hash",
			Table:   "index_test",
			Schema:  "public",
			Columns: []string{"name"},
			Unique:  false,
			Type:    "hash",
		}); err != nil {
			t.Fatalf("CreateIndex hash: %v", err)
		}

		indexes, _ := introspector.GetIndexes(ctx, appDB, "public", "index_test")
		for _, idx := range indexes {
			if idx.Name == "idx_name_hash" {
				if idx.Type != "hash" {
					t.Errorf("expected hash, got %s", idx.Type)
				}
			}
		}
	})

	t.Run("create gin index", func(t *testing.T) {
		if err := introspector.CreateIndex(ctx, appDB, CreateIndexRequest{
			Name:    "idx_tags_gin",
			Table:   "index_test",
			Schema:  "public",
			Columns: []string{"tags"},
			Unique:  false,
			Type:    "gin",
		}); err != nil {
			t.Fatalf("CreateIndex gin: %v", err)
		}

		indexes, _ := introspector.GetIndexes(ctx, appDB, "public", "index_test")
		for _, idx := range indexes {
			if idx.Name == "idx_tags_gin" {
				if idx.Type != "gin" {
					t.Errorf("expected gin, got %s", idx.Type)
				}
			}
		}
	})

	t.Run("invalid index type rejected", func(t *testing.T) {
		err := introspector.CreateIndex(ctx, appDB, CreateIndexRequest{
			Name:    "idx_bad",
			Table:   "index_test",
			Schema:  "public",
			Columns: []string{"email"},
			Type:    "invalid_type",
		})
		if err == nil {
			t.Fatal("expected error for invalid index type")
		}
	})

	t.Run("empty columns rejected", func(t *testing.T) {
		err := introspector.CreateIndex(ctx, appDB, CreateIndexRequest{
			Name:    "idx_nocols",
			Table:   "index_test",
			Schema:  "public",
			Columns: []string{},
			Type:    "btree",
		})
		if err == nil {
			t.Fatal("expected error for empty columns")
		}
	})

	t.Run("drop index", func(t *testing.T) {
		if err := introspector.DropIndex(ctx, appDB, "public", "idx_email"); err != nil {
			t.Fatalf("DropIndex: %v", err)
		}

		indexes, _ := introspector.GetIndexes(ctx, appDB, "public", "index_test")
		for _, idx := range indexes {
			if idx.Name == "idx_email" {
				t.Error("index should be dropped")
			}
		}
	})
}

func TestIntegration_Types(t *testing.T) {
	_, appDB, cleanup := setupPG(t)
	defer cleanup()

	introspector := NewIntrospector()
	ctx := context.Background()

	// Create an enum type
	_, err := appDB.ExecContext(ctx, "CREATE TYPE mood AS ENUM ('sad', 'ok', 'happy')")
	if err != nil {
		t.Fatalf("create enum type: %v", err)
	}

	// Create a composite type
	_, err = appDB.ExecContext(ctx, "CREATE TYPE address AS (street text, city text, zip text)")
	if err != nil {
		t.Fatalf("create composite type: %v", err)
	}

	// Create a domain type
	_, err = appDB.ExecContext(ctx, "CREATE DOMAIN positive_int AS integer CHECK (VALUE > 0)")
	if err != nil {
		t.Fatalf("create domain type: %v", err)
	}

	t.Run("get types returns enum with values", func(t *testing.T) {
		types, err := introspector.GetTypes(ctx, appDB, "public")
		if err != nil {
			t.Fatalf("GetTypes: %v", err)
		}

		foundEnum := false
		foundComposite := false
		foundDomain := false

		for _, pt := range types {
			switch pt.Name {
			case "mood":
				foundEnum = true
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
			case "address":
				foundComposite = true
				if pt.Type != "composite" {
					t.Errorf("expected type composite, got %s", pt.Type)
				}
				if len(pt.Values) != 0 {
					t.Errorf("composite type should have empty values, got %v", pt.Values)
				}
			case "positive_int":
				foundDomain = true
				if pt.Type != "domain" {
					t.Errorf("expected type domain, got %s", pt.Type)
				}
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
