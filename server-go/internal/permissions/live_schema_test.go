package permissions

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// EXC-649: a permission naming a table, column or relationship the database
// does not have is refused when it is written, with the reason the engine
// would log as permission_invalid, instead of being saved and dropped later.

func cornerShop(name, database string) *LiveSchema {
	return NewLiveSchema(name, database, []TableShape{
		{Name: "products", Columns: []string{"id", "name", "price"}},
		{Name: "orders", Columns: []string{"id", "customer_id", "status"}},
		{Name: "order_items", Columns: []string{"id", "order_id", "product_id", "qty"}},
		{Name: "product_stats", View: true, Columns: []string{"product_id", "sold"}},
	}, []KeyColumn{
		{Constraint: "items_order", SourceSchema: name, SourceTable: "order_items", SourceColumn: "order_id", TargetSchema: name, TargetTable: "orders"},
		{Constraint: "items_product", SourceSchema: name, SourceTable: "order_items", SourceColumn: "product_id", TargetSchema: name, TargetTable: "products"},
	})
}

func sourceOf(schemas ...*LiveSchema) SchemaSource {
	return func(_ context.Context, schemaName string) (*LiveSchema, error) {
		for _, live := range schemas {
			if live.Name == schemaName {
				return live, nil
			}
		}
		return NewLiveSchema(schemaName, "", nil, nil), nil
	}
}

func checkReason(t *testing.T, source SchemaSource, table, operation, body string) string {
	t.Helper()
	err := CheckLive(context.Background(), source, table, operation, json.RawMessage(body))
	if err == nil {
		return ""
	}
	var invalid *InvalidPermissionError
	if !errors.As(err, &invalid) {
		t.Fatalf("%s %s: not a permission_invalid refusal: %v", table, operation, err)
	}
	if !strings.HasPrefix(err.Error(), "permission_invalid: ") {
		t.Fatalf("error %q does not carry the engine's code", err)
	}
	return invalid.Reason
}

func TestCheckLive_AcceptsWhatTheSchemaHas(t *testing.T) {
	source := sourceOf(cornerShop("app", "app"))
	for _, c := range []struct{ table, op, body string }{
		{"app.products", "select", `{"filter":{},"columns":"*"}`},
		{"app.order_items", "select", `{"filter":{"appOrder":{"status":{"_eq":"paid"}}},"columns":["id","qty"]}`},
		{"app.order_items", "select", `{"filter":{"appOrderId":{"customer_id":{"_eq":"X-Excalibase-User-Id"}}},"columns":"*"}`},
		{"app.orders", "select", `{"filter":{"appOrderItems":{"qty":{"_gt":1}}},"columns":"*"}`},
		{"app.orders", "insert", `{"check":{"_and":[{"status":{"_eq":"draft"}}]},"columns":["status"],"set":{"customer_id":"X-Excalibase-User-Id"}}`},
		{"app.orders", "delete", `{"filter":{"_exists":{"_table":"app.products","_where":{"price":{"_gt":0}}}}}`},
		{"app.product_stats", "select", `{"filter":{},"columns":["sold"]}`},
	} {
		if reason := checkReason(t, source, c.table, c.op, c.body); reason != "" {
			t.Errorf("%s %s %s refused: %s", c.table, c.op, c.body, reason)
		}
	}
}

func TestCheckLive_RefusesWhatTheSchemaLacks(t *testing.T) {
	source := sourceOf(cornerShop("app", "app"))
	for _, c := range []struct{ table, op, body, want string }{
		{"app.nope", "select", `{"filter":{},"columns":"*"}`, "Unknown table app.nope"},
		{"app.products", "select", `{"filter":{},"columns":["id","colour"]}`, "Unknown column colour on app.products"},
		{"app.products", "select", `{"filter":{"colour":{"_eq":"red"}},"columns":"*"}`, "Unknown column colour on app.products"},
		{"app.order_items", "select", `{"filter":{"orders":{"status":{"_eq":"paid"}}},"columns":"*"}`, "Unknown relationship orders on app.order_items"},
		{"app.order_items", "select", `{"filter":{"appOrder":{"state":{"_eq":"paid"}}},"columns":"*"}`, "Unknown column state on app.orders"},
		{"app.orders", "update", `{"filter":{"_or":[{"_not":{"ghost":{"_is_null":true}}}]},"columns":"*"}`, "Unknown column ghost on app.orders"},
		{"app.orders", "insert", `{"check":{},"columns":"*","set":{"owner":"X-Excalibase-User-Id"}}`, "Unknown column owner on app.orders"},
		{"app.orders", "delete", `{"filter":{"_exists":{"_table":"app.ghosts","_where":{}}}}`, "Unknown table app.ghosts"},
		{"app.product_stats", "insert", `{"check":{},"columns":"*"}`, "app.product_stats is a view"},
	} {
		reason := checkReason(t, source, c.table, c.op, c.body)
		if !strings.Contains(reason, c.want) {
			t.Errorf("%s %s %s: reason %q, want %q", c.table, c.op, c.body, reason, c.want)
		}
	}
}

// A MySQL project's tables live in its one database; public names nothing.
func TestCheckLive_NamesTheDatabaseForAMySQLProjectsPublicTable(t *testing.T) {
	source := sourceOf(cornerShop("app", "app"), NewLiveSchema("public", "app", nil, nil))
	reason := checkReason(t, source, "public.products", "select", `{"filter":{},"columns":"*"}`)
	if !strings.Contains(reason, "Unknown table public.products") || !strings.Contains(reason, "app.products") {
		t.Fatalf("reason = %q", reason)
	}
}

// A key to or from another schema is named by rules this check does not
// model, so a name it cannot place on such a table is left to the engine.
func TestCheckLive_LeavesCrossSchemaRelationshipsToTheEngine(t *testing.T) {
	live := NewLiveSchema("public", "", []TableShape{{Name: "orders", Columns: []string{"id", "user_id"}}}, []KeyColumn{
		{Constraint: "orders_user", SourceSchema: "public", SourceTable: "orders", SourceColumn: "user_id", TargetSchema: "auth", TargetTable: "users"},
	})
	if reason := checkReason(t, sourceOf(live), "public.orders", "select", `{"filter":{"authUser":{"id":{"_eq":1}}},"columns":"*"}`); reason != "" {
		t.Fatalf("refused: %s", reason)
	}
}

func TestCheckLive_PassesOnAFailedSchemaRead(t *testing.T) {
	failing := func(context.Context, string) (*LiveSchema, error) { return nil, errors.New("connection refused") }
	err := CheckLive(context.Background(), failing, "app.products", "select", json.RawMessage(`{"filter":{},"columns":"*"}`))
	var invalid *InvalidPermissionError
	if err == nil || errors.As(err, &invalid) {
		t.Fatalf("err = %v, want the read failure itself", err)
	}
}
