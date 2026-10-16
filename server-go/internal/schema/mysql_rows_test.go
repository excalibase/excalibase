package schema

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"
)

var ordersTable = mysqlTable{name: "orders", columns: map[string]string{
	"id": "id", "status": "status", "note": "note", "deleted_at": "deleted_at", "total": "total", "meta": "meta",
}}

func TestBuildMySQLSelect(t *testing.T) {
	dataSQL, countSQL, dataArgs, countArgs, err := buildMySQLSelect(ordersTable, RowQueryOpts{
		Limit: 20, Offset: 40, Sort: "id", Order: "desc",
		Filters: []RowFilter{{Column: "status", Operator: "=", Value: "paid"}, {Column: "note", Operator: "ilike", Value: "%Rush%"}, {Column: "deleted_at", Operator: "is_null"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	where := " WHERE `status` = ? AND LOWER(`note`) LIKE LOWER(?) AND `deleted_at` IS NULL"
	if countSQL != "SELECT COUNT(*) FROM `orders`"+where {
		t.Errorf("count: %s", countSQL)
	}
	if dataSQL != "SELECT * FROM `orders`"+where+" ORDER BY `id` DESC LIMIT ? OFFSET ?" {
		t.Errorf("data: %s", dataSQL)
	}
	if !reflect.DeepEqual(countArgs, []interface{}{"paid", "%Rush%"}) || !reflect.DeepEqual(dataArgs, []interface{}{"paid", "%Rush%", 20, 40}) {
		t.Errorf("args: %v %v", countArgs, dataArgs)
	}
	if _, _, _, _, err := buildMySQLSelect(ordersTable, RowQueryOpts{Filters: []RowFilter{{Column: "status", Operator: "~"}}}); err == nil {
		t.Error("an unknown operator was accepted")
	}
}

// Only the catalogue's own names reach the SQL.
func TestMySQLRowSQLRefusesAColumnTheTableLacks(t *testing.T) {
	var input *InputError
	if _, _, _, _, err := buildMySQLSelect(ordersTable, RowQueryOpts{Sort: "id`; DROP TABLE x"}); !errors.As(err, &input) {
		t.Errorf("sort: %v", err)
	}
	if _, _, err := buildMySQLInsert(ordersTable, map[string]interface{}{"nope": 1}); !errors.As(err, &input) {
		t.Errorf("insert: %v", err)
	}
}

func TestBuildMySQLInsert(t *testing.T) {
	statement, args, err := buildMySQLInsert(ordersTable, map[string]interface{}{"total": json.Number("12345678901234567890"), "meta": map[string]interface{}{"a": 1.0}})
	if err != nil {
		t.Fatal(err)
	}
	if statement != "INSERT INTO `orders` (`meta`, `total`) VALUES (?, ?)" || !reflect.DeepEqual(args, []interface{}{`{"a":1}`, "12345678901234567890"}) {
		t.Fatalf("%s %v", statement, args)
	}
	statement, args, _ = buildMySQLInsert(ordersTable, map[string]interface{}{})
	if statement != "INSERT INTO `orders` () VALUES ()" || len(args) != 0 {
		t.Fatalf("defaults: %s %v", statement, args)
	}
}

func TestBuildMySQLKeyedStatements(t *testing.T) {
	update, args, err := buildMySQLUpdate(ordersTable, map[string]interface{}{"status": "paid", "note": nil}, "id")
	if err != nil || update != "UPDATE `orders` SET `note` = ?, `status` = ? WHERE `id` = ?" || len(args) != 2 {
		t.Errorf("update: %s %v %v", update, args, err)
	}
	remove, err := buildMySQLDelete(ordersTable, "id")
	if err != nil || remove != "DELETE FROM `orders` WHERE `id` = ?" {
		t.Errorf("delete: %s %v", remove, err)
	}
	read, err := buildMySQLReadBack(ordersTable, []string{"id"})
	if err != nil || read != "SELECT * FROM `orders` WHERE `id` = ?" {
		t.Errorf("read back: %s %v", read, err)
	}
	if _, err := buildMySQLDelete(ordersTable, "nope"); err == nil {
		t.Error("an unknown key column was accepted")
	}
}
