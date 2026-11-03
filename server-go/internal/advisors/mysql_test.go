package advisors

import (
	"errors"
	"fmt"
	"testing"

	"github.com/go-sql-driver/mysql"
)

func TestMySQLUnusedIndexes(t *testing.T) {
	indexes := []mysqlIndex{
		{Table: "orders", Name: "PRIMARY", Columns: []string{"id"}, Unique: true},
		{Table: "orders", Name: "orders_note", Columns: []string{"note"}},
		{Table: "orders", Name: "orders_code", Columns: []string{"code"}, Unique: true},
		{Table: "orders", Name: "orders_customer", Columns: []string{"customer_id", "created_at"}},
		{Table: "orders", Name: "orders_used", Columns: []string{"status"}},
		{Table: "idle", Name: "idle_note", Columns: []string{"note"}},
	}
	foreignKeys := map[string][][]string{"orders": {{"customer_id"}}}
	usage := []indexUse{
		{Table: "orders", Reads: 50},
		{Table: "orders", Index: "PRIMARY"},
		{Table: "orders", Index: "orders_note"},
		{Table: "orders", Index: "orders_code"},
		{Table: "orders", Index: "orders_customer"},
		{Table: "orders", Index: "orders_used", Stars: 7, Reads: 7},
		// A table nothing has read says nothing about its indexes.
		{Table: "idle", Index: "idle_note"},
	}
	got := mysqlUnusedIndexes("app", indexes, foreignKeys, usage)
	if len(got) != 1 || got[0].Index != "orders_note" || got[0].Object != "app.orders" {
		t.Fatalf("findings = %+v", got)
	}
}

func TestMySQLLargeTables(t *testing.T) {
	rows := map[string]int64{"events": 500_000, "lookups": 400_000, "small": 10, "unread": 900_000}
	usage := []indexUse{
		{Table: "events", Reads: 2_000_000},
		{Table: "events", Index: "PRIMARY", Stars: 10, Reads: 10},
		{Table: "lookups", Reads: 400_000},
		{Table: "lookups", Index: "PRIMARY", Stars: 900_000, Reads: 900_000},
		{Table: "small", Reads: 1000},
	}
	got := mysqlLargeTables("app", rows, usage, 100_000)
	if len(got) != 1 || got[0].Object != "app.events" {
		t.Fatalf("findings = %+v", got)
	}
	if got[0].Check != CheckLargeTableNoIndexUse {
		t.Fatalf("finding = %+v", got[0])
	}
}

func TestMySQLDuplicateRows(t *testing.T) {
	got := mysqlDuplicateRows("app", []mysqlIndex{
		{Table: "t", Name: "PRIMARY", Columns: []string{"id"}, Unique: true, Type: "BTREE"},
		{Table: "t", Name: "t_id", Columns: []string{"id"}, Type: "BTREE"},
		{Table: "t", Name: "t_id_name", Columns: []string{"id", "name"}, Type: "BTREE"},
		{Table: "t", Name: "t_ft", Columns: []string{"id"}, Type: "FULLTEXT"},
	})
	keys := map[string]string{}
	for _, row := range got {
		keys[row.Index] = row.Key
	}
	if keys["PRIMARY"] != keys["t_id"] || keys["t_id"] == keys["t_id_name"] || keys["t_id"] == keys["t_ft"] {
		t.Fatalf("keys = %v", keys)
	}
	for _, row := range got {
		if row.Index == "PRIMARY" && !row.Primary {
			t.Fatalf("PRIMARY not marked: %+v", row)
		}
	}
}

func TestPerformanceSchemaDenialIsUnavailable(t *testing.T) {
	for _, number := range []uint16{1142, 1044, 1227} {
		err := fmt.Errorf("read usage: %w", &mysql.MySQLError{Number: number, Message: "denied"})
		if !accessDenied(err) {
			t.Errorf("%d not read as denied", number)
		}
	}
	if accessDenied(errors.New("boom")) || accessDenied(&mysql.MySQLError{Number: 1064}) {
		t.Fatal("other errors read as denied")
	}
}
