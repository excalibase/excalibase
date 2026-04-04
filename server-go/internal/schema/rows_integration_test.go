//go:build integration

package schema

import (
	"context"
	"testing"
)

func TestIntegration_GetRows(t *testing.T) {
	_, appDB, cleanup := setupPG(t)
	defer cleanup()

	introspector := NewIntrospector()
	ctx := context.Background()

	// Create and seed table
	_, err := appDB.ExecContext(ctx, `
		CREATE TABLE row_test (
			id serial PRIMARY KEY,
			name text NOT NULL,
			age int,
			email text UNIQUE
		)
	`)
	if err != nil {
		t.Fatalf("create table: %v", err)
	}
	_, err = appDB.ExecContext(ctx, `
		INSERT INTO row_test (name, age, email) VALUES
			('Alice', 30, 'alice@test.com'),
			('Bob', 25, 'bob@test.com'),
			('Charlie', 35, 'charlie@test.com'),
			('Diana', 28, 'diana@test.com'),
			('Eve', 40, 'eve@test.com')
	`)
	if err != nil {
		t.Fatalf("seed data: %v", err)
	}

	t.Run("default pagination", func(t *testing.T) {
		result, err := introspector.GetRows(ctx, appDB, "public", "row_test", RowQueryOpts{})
		if err != nil {
			t.Fatalf("GetRows: %v", err)
		}
		if result.TotalCount != 5 {
			t.Errorf("expected totalCount 5, got %d", result.TotalCount)
		}
		if len(result.Rows) != 5 {
			t.Errorf("expected 5 rows, got %d", len(result.Rows))
		}
		if len(result.Columns) != 4 {
			t.Errorf("expected 4 columns, got %d", len(result.Columns))
		}
	})

	t.Run("limit and offset", func(t *testing.T) {
		result, err := introspector.GetRows(ctx, appDB, "public", "row_test", RowQueryOpts{
			Limit:  2,
			Offset: 1,
			Sort:   "id",
			Order:  "asc",
		})
		if err != nil {
			t.Fatalf("GetRows: %v", err)
		}
		if result.TotalCount != 5 {
			t.Errorf("expected totalCount 5, got %d", result.TotalCount)
		}
		if len(result.Rows) != 2 {
			t.Errorf("expected 2 rows, got %d", len(result.Rows))
		}
	})

	t.Run("limit capped at 1000", func(t *testing.T) {
		// Should not error, just cap
		result, err := introspector.GetRows(ctx, appDB, "public", "row_test", RowQueryOpts{
			Limit: 5000,
		})
		if err != nil {
			t.Fatalf("GetRows: %v", err)
		}
		if result.TotalCount != 5 {
			t.Errorf("expected totalCount 5, got %d", result.TotalCount)
		}
	})
}

func TestIntegration_GetRows_Sort(t *testing.T) {
	_, appDB, cleanup := setupPG(t)
	defer cleanup()

	introspector := NewIntrospector()
	ctx := context.Background()

	_, err := appDB.ExecContext(ctx, `
		CREATE TABLE sort_test (
			id serial PRIMARY KEY,
			name text NOT NULL,
			age int
		)
	`)
	if err != nil {
		t.Fatalf("create table: %v", err)
	}
	_, err = appDB.ExecContext(ctx, `
		INSERT INTO sort_test (name, age) VALUES
			('Alice', 30),
			('Bob', 25),
			('Charlie', 35)
	`)
	if err != nil {
		t.Fatalf("seed: %v", err)
	}

	t.Run("sort ascending by age", func(t *testing.T) {
		result, err := introspector.GetRows(ctx, appDB, "public", "sort_test", RowQueryOpts{
			Sort:  "age",
			Order: "asc",
		})
		if err != nil {
			t.Fatalf("GetRows: %v", err)
		}
		if len(result.Rows) != 3 {
			t.Fatalf("expected 3 rows, got %d", len(result.Rows))
		}
		// Age column is index 2. First row should be Bob (25)
		firstAge := result.Rows[0][2]
		if toInt64(firstAge) != 25 {
			t.Errorf("expected first age 25, got %v", firstAge)
		}
	})

	t.Run("sort descending by age", func(t *testing.T) {
		result, err := introspector.GetRows(ctx, appDB, "public", "sort_test", RowQueryOpts{
			Sort:  "age",
			Order: "desc",
		})
		if err != nil {
			t.Fatalf("GetRows: %v", err)
		}
		firstAge := result.Rows[0][2]
		if toInt64(firstAge) != 35 {
			t.Errorf("expected first age 35, got %v", firstAge)
		}
	})

	t.Run("invalid sort order rejected", func(t *testing.T) {
		_, err := introspector.GetRows(ctx, appDB, "public", "sort_test", RowQueryOpts{
			Sort:  "age",
			Order: "DROP TABLE sort_test;--",
		})
		if err == nil {
			t.Fatal("expected error for invalid sort order")
		}
	})
}

func TestIntegration_GetRows_Filter(t *testing.T) {
	_, appDB, cleanup := setupPG(t)
	defer cleanup()

	introspector := NewIntrospector()
	ctx := context.Background()

	_, err := appDB.ExecContext(ctx, `
		CREATE TABLE filter_test (
			id serial PRIMARY KEY,
			name text NOT NULL,
			age int,
			email text
		)
	`)
	if err != nil {
		t.Fatalf("create table: %v", err)
	}
	_, err = appDB.ExecContext(ctx, `
		INSERT INTO filter_test (name, age, email) VALUES
			('Alice', 30, 'alice@test.com'),
			('Bob', 25, NULL),
			('Charlie', 35, 'charlie@test.com')
	`)
	if err != nil {
		t.Fatalf("seed: %v", err)
	}

	t.Run("filter equals", func(t *testing.T) {
		result, err := introspector.GetRows(ctx, appDB, "public", "filter_test", RowQueryOpts{
			Filters: []RowFilter{
				{Column: "name", Operator: "=", Value: "Alice"},
			},
		})
		if err != nil {
			t.Fatalf("GetRows: %v", err)
		}
		if len(result.Rows) != 1 {
			t.Fatalf("expected 1 row, got %d", len(result.Rows))
		}
		if result.TotalCount != 1 {
			t.Errorf("expected totalCount 1, got %d", result.TotalCount)
		}
	})

	t.Run("filter greater than", func(t *testing.T) {
		result, err := introspector.GetRows(ctx, appDB, "public", "filter_test", RowQueryOpts{
			Filters: []RowFilter{
				{Column: "age", Operator: ">", Value: "28"},
			},
		})
		if err != nil {
			t.Fatalf("GetRows: %v", err)
		}
		if len(result.Rows) != 2 {
			t.Errorf("expected 2 rows, got %d", len(result.Rows))
		}
	})

	t.Run("filter is_null", func(t *testing.T) {
		result, err := introspector.GetRows(ctx, appDB, "public", "filter_test", RowQueryOpts{
			Filters: []RowFilter{
				{Column: "email", Operator: "is_null"},
			},
		})
		if err != nil {
			t.Fatalf("GetRows: %v", err)
		}
		if len(result.Rows) != 1 {
			t.Errorf("expected 1 row (Bob), got %d", len(result.Rows))
		}
	})

	t.Run("filter is_not_null", func(t *testing.T) {
		result, err := introspector.GetRows(ctx, appDB, "public", "filter_test", RowQueryOpts{
			Filters: []RowFilter{
				{Column: "email", Operator: "is_not_null"},
			},
		})
		if err != nil {
			t.Fatalf("GetRows: %v", err)
		}
		if len(result.Rows) != 2 {
			t.Errorf("expected 2 rows, got %d", len(result.Rows))
		}
	})

	t.Run("filter like", func(t *testing.T) {
		result, err := introspector.GetRows(ctx, appDB, "public", "filter_test", RowQueryOpts{
			Filters: []RowFilter{
				{Column: "name", Operator: "like", Value: "%li%"},
			},
		})
		if err != nil {
			t.Fatalf("GetRows: %v", err)
		}
		// Alice and Charlie match %li%
		if len(result.Rows) != 2 {
			t.Errorf("expected 2 rows, got %d", len(result.Rows))
		}
	})

	t.Run("multiple filters AND", func(t *testing.T) {
		result, err := introspector.GetRows(ctx, appDB, "public", "filter_test", RowQueryOpts{
			Filters: []RowFilter{
				{Column: "age", Operator: ">=", Value: "30"},
				{Column: "email", Operator: "is_not_null"},
			},
		})
		if err != nil {
			t.Fatalf("GetRows: %v", err)
		}
		// Alice (30, has email) and Charlie (35, has email)
		if len(result.Rows) != 2 {
			t.Errorf("expected 2 rows, got %d", len(result.Rows))
		}
	})

	t.Run("invalid operator rejected", func(t *testing.T) {
		_, err := introspector.GetRows(ctx, appDB, "public", "filter_test", RowQueryOpts{
			Filters: []RowFilter{
				{Column: "name", Operator: "DROP TABLE", Value: "x"},
			},
		})
		if err == nil {
			t.Fatal("expected error for invalid operator")
		}
	})
}

func TestIntegration_InsertRow(t *testing.T) {
	_, appDB, cleanup := setupPG(t)
	defer cleanup()

	introspector := NewIntrospector()
	ctx := context.Background()

	_, err := appDB.ExecContext(ctx, `
		CREATE TABLE insert_test (
			id serial PRIMARY KEY,
			name text NOT NULL,
			age int
		)
	`)
	if err != nil {
		t.Fatalf("create table: %v", err)
	}

	t.Run("insert and verify", func(t *testing.T) {
		result, err := introspector.InsertRow(ctx, appDB, "public", "insert_test", map[string]interface{}{
			"name": "Alice",
			"age":  30,
		})
		if err != nil {
			t.Fatalf("InsertRow: %v", err)
		}
		if len(result.Rows) != 1 {
			t.Fatalf("expected 1 returned row, got %d", len(result.Rows))
		}
		if len(result.Columns) < 3 {
			t.Fatalf("expected at least 3 columns, got %d", len(result.Columns))
		}

		// Verify data is actually in the table
		rows, err := introspector.GetRows(ctx, appDB, "public", "insert_test", RowQueryOpts{})
		if err != nil {
			t.Fatalf("GetRows: %v", err)
		}
		if rows.TotalCount != 1 {
			t.Errorf("expected 1 row, got %d", rows.TotalCount)
		}
	})

	t.Run("insert multiple rows", func(t *testing.T) {
		for i := 0; i < 3; i++ {
			_, err := introspector.InsertRow(ctx, appDB, "public", "insert_test", map[string]interface{}{
				"name": "User",
				"age":  20 + i,
			})
			if err != nil {
				t.Fatalf("InsertRow %d: %v", i, err)
			}
		}

		rows, err := introspector.GetRows(ctx, appDB, "public", "insert_test", RowQueryOpts{})
		if err != nil {
			t.Fatalf("GetRows: %v", err)
		}
		if rows.TotalCount != 4 { // 1 from first test + 3
			t.Errorf("expected 4 rows, got %d", rows.TotalCount)
		}
	})
}

func TestIntegration_UpdateRow(t *testing.T) {
	_, appDB, cleanup := setupPG(t)
	defer cleanup()

	introspector := NewIntrospector()
	ctx := context.Background()

	_, err := appDB.ExecContext(ctx, `
		CREATE TABLE update_test (
			id serial PRIMARY KEY,
			name text NOT NULL,
			age int
		)
	`)
	if err != nil {
		t.Fatalf("create table: %v", err)
	}
	_, err = appDB.ExecContext(ctx, `INSERT INTO update_test (name, age) VALUES ('Alice', 30)`)
	if err != nil {
		t.Fatalf("seed: %v", err)
	}

	t.Run("update and verify", func(t *testing.T) {
		err := introspector.UpdateRow(ctx, appDB, "public", "update_test", "id", "1", map[string]interface{}{
			"name": "Alice Updated",
			"age":  31,
		})
		if err != nil {
			t.Fatalf("UpdateRow: %v", err)
		}

		// Verify the change
		result, err := introspector.GetRows(ctx, appDB, "public", "update_test", RowQueryOpts{
			Filters: []RowFilter{
				{Column: "id", Operator: "=", Value: "1"},
			},
		})
		if err != nil {
			t.Fatalf("GetRows: %v", err)
		}
		if len(result.Rows) != 1 {
			t.Fatalf("expected 1 row, got %d", len(result.Rows))
		}
		// name is column index 1
		if result.Rows[0][1] != "Alice Updated" {
			t.Errorf("expected 'Alice Updated', got %v", result.Rows[0][1])
		}
	})

	t.Run("update non-existent row", func(t *testing.T) {
		err := introspector.UpdateRow(ctx, appDB, "public", "update_test", "id", "999", map[string]interface{}{
			"name": "Ghost",
		})
		// Should not error (0 rows affected is valid SQL behavior)
		if err != nil {
			t.Fatalf("UpdateRow non-existent: %v", err)
		}
	})
}

func TestIntegration_DeleteRow(t *testing.T) {
	_, appDB, cleanup := setupPG(t)
	defer cleanup()

	introspector := NewIntrospector()
	ctx := context.Background()

	_, err := appDB.ExecContext(ctx, `
		CREATE TABLE delete_test (
			id serial PRIMARY KEY,
			name text NOT NULL
		)
	`)
	if err != nil {
		t.Fatalf("create table: %v", err)
	}
	_, err = appDB.ExecContext(ctx, `
		INSERT INTO delete_test (name) VALUES ('Alice'), ('Bob'), ('Charlie')
	`)
	if err != nil {
		t.Fatalf("seed: %v", err)
	}

	t.Run("delete and verify", func(t *testing.T) {
		err := introspector.DeleteRow(ctx, appDB, "public", "delete_test", "id", "2")
		if err != nil {
			t.Fatalf("DeleteRow: %v", err)
		}

		result, err := introspector.GetRows(ctx, appDB, "public", "delete_test", RowQueryOpts{
			Sort:  "id",
			Order: "asc",
		})
		if err != nil {
			t.Fatalf("GetRows: %v", err)
		}
		if result.TotalCount != 2 {
			t.Errorf("expected 2 rows after delete, got %d", result.TotalCount)
		}
		// Verify Bob (id=2) is gone
		for _, row := range result.Rows {
			if row[1] == "Bob" {
				t.Error("Bob should have been deleted")
			}
		}
	})

	t.Run("delete non-existent row", func(t *testing.T) {
		err := introspector.DeleteRow(ctx, appDB, "public", "delete_test", "id", "999")
		if err != nil {
			t.Fatalf("DeleteRow non-existent: %v", err)
		}
	})
}

// toInt64 converts various numeric types to int64 for comparison.
func toInt64(v interface{}) int64 {
	switch n := v.(type) {
	case int64:
		return n
	case int32:
		return int64(n)
	case int:
		return int64(n)
	case float64:
		return int64(n)
	default:
		return -1
	}
}
