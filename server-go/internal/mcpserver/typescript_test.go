package mcpserver

import (
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/schema"
)

func TestTypeScriptForMapsPostgresTypes(t *testing.T) {
	tables := []tableColumns{{
		Name: "order_items",
		Columns: []schema.ColumnInfo{
			{Name: "id", DataType: "bigint"},
			{Name: "price", DataType: "numeric", Nullable: true},
			{Name: "qty", DataType: "integer"},
			{Name: "paid", DataType: "boolean"},
			{Name: "meta", DataType: "jsonb", Nullable: true},
			{Name: "tags", DataType: "ARRAY", ElementType: "text"},
			{Name: "counts", DataType: "ARRAY", ElementType: "bigint"},
			{Name: "prices", DataType: "ARRAY", ElementType: "numeric", Nullable: true},
			{Name: "scores", DataType: "ARRAY", ElementType: "integer"},
			{Name: "docs", DataType: "ARRAY", ElementType: "jsonb"},
			{Name: "legacy", DataType: "ARRAY"},
			{Name: "created_at", DataType: "timestamp with time zone"},
			{Name: "status", DataType: "USER-DEFINED"},
			{Name: "weird-name", DataType: "text"},
		},
	}}
	got := typeScriptFor("public", tables)
	for _, want := range []string{
		"export interface OrderItems {",
		"  id: string;",
		"  price: string | null;",
		"  qty: number;",
		"  paid: boolean;",
		"  meta: Json | null;",
		"  tags: string[];",
		"  counts: string[];",
		"  prices: string[] | null;",
		"  scores: number[];",
		"  docs: Json[];",
		"  legacy: unknown[];",
		"  created_at: string;",
		"  status: string;",
		`  "weird-name": string;`,
		`export interface Database {`,
		`    order_items: OrderItems;`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
}

func TestTypeScriptForNamesNeverCollide(t *testing.T) {
	got := typeScriptFor("public", []tableColumns{{Name: "users"}, {Name: "Users"}})
	if !strings.Contains(got, "export interface Users {") || !strings.Contains(got, "export interface Users2 {") {
		t.Fatalf("got:\n%s", got)
	}
}
