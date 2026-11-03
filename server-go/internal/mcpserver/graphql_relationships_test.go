package mcpserver

import (
	"net/http"
	"reflect"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/schema"
)

// EXC-641: relationship fields carry the engine's names (SchemaInfo +
// RelationshipAliases), the ones nested queries and permission filters use.

func fk(constraint, source, column, target string) schema.RelationshipInfo {
	return schema.RelationshipInfo{ConstraintName: constraint, SourceTable: source, SourceColumn: column, TargetTable: target, TargetColumn: "id"}
}

func tablesNamed(names ...string) []tableColumns {
	out := make([]tableColumns, 0, len(names))
	for _, name := range names {
		out = append(out, tableColumns{Name: name, Columns: []schema.ColumnInfo{{Name: "id"}}})
	}
	return out
}

func TestRelationshipFields_ObjectIsNamedWithoutIdAndArrayAfterTheChildTable(t *testing.T) {
	got := relationshipFields("hana", tablesNamed("orders", "customer"), []schema.RelationshipInfo{fk("orders_customer_fk", "orders", "customer_id", "customer")})
	want := map[string][]relationshipField{
		"orders":   {{Field: "hanaCustomer", Kind: "object", Table: "customer", Columns: []string{"customer_id"}, DeprecatedName: "hanaCustomerId"}},
		"customer": {{Field: "hanaOrders", Kind: "array", Table: "orders", Columns: []string{"customer_id"}}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v\nwant %+v", got, want)
	}
}

func TestRelationshipFields_SeveralKeysToOneTableAreNamedByKey(t *testing.T) {
	got := relationshipFields("public", tablesNamed("matches", "teams"), []schema.RelationshipInfo{
		fk("matches_home_fk", "matches", "home_team_id", "teams"),
		fk("matches_away_fk", "matches", "away_team_id", "teams"),
	})
	want := []relationshipField{
		{Field: "publicMatchesByAwayTeam", Kind: "array", Table: "matches", Columns: []string{"away_team_id"}, DeprecatedName: "publicMatchesAwayTeamId"},
		{Field: "publicMatchesByHomeTeam", Kind: "array", Table: "matches", Columns: []string{"home_team_id"}, DeprecatedName: "publicMatchesHomeTeamId"},
	}
	if !reflect.DeepEqual(got["teams"], want) {
		t.Fatalf("teams = %+v", got["teams"])
	}
	if got["matches"][0].Field != "publicAwayTeam" || got["matches"][1].Field != "publicHomeTeam" {
		t.Fatalf("matches = %+v", got["matches"])
	}
}

func TestRelationshipFields_CompositeKeyIsNamedAfterTheTables(t *testing.T) {
	got := relationshipFields("public", tablesNamed("line_items", "orders"), []schema.RelationshipInfo{
		fk("line_items_order_fk", "line_items", "order_id", "orders"),
		fk("line_items_order_fk", "line_items", "order_region", "orders"),
	})
	if want := []relationshipField{{Field: "publicOrders", Kind: "object", Table: "orders", Columns: []string{"order_id", "order_region"}}}; !reflect.DeepEqual(got["line_items"], want) {
		t.Fatalf("line_items = %+v", got["line_items"])
	}
	if got["orders"][0].Field != "publicLineItems" {
		t.Fatalf("orders = %+v", got["orders"])
	}
}

func TestRelationshipFields_AnAliasThatClashesWithAColumnIsNotOffered(t *testing.T) {
	tables := []tableColumns{
		{Name: "tasks", Columns: []schema.ColumnInfo{{Name: "id"}, {Name: "publicOwner"}}},
		{Name: "owner", Columns: []schema.ColumnInfo{{Name: "id"}}},
	}
	got := relationshipFields("public", tables, []schema.RelationshipInfo{fk("tasks_owner_fk", "tasks", "owner_id", "owner")})
	if want := []relationshipField{{Field: "publicOwnerId", Kind: "object", Table: "owner", Columns: []string{"owner_id"}}}; !reflect.DeepEqual(got["tasks"], want) {
		t.Fatalf("tasks = %+v", got["tasks"])
	}
}

func TestGetGraphQLSchemaListsEachTablesRelationships(t *testing.T) {
	routes := newFakeRoutes()
	routes.on(http.MethodGet, schemaA+"/tables", 200, `[{"name":"tasks","schema":"public","type":"table"},{"name":"projects","schema":"public","type":"table"}]`)
	routes.on(http.MethodGet, schemaA+"/tables/tasks/columns", 200, `[{"name":"id"},{"name":"project_id"}]`)
	routes.on(http.MethodGet, schemaA+"/tables/projects/columns", 200, `[{"name":"id"}]`)
	routes.on(http.MethodGet, schemaA+"/relationships", 200, `[{"constraintName":"tasks_project_fk","sourceTable":"tasks","sourceColumn":"project_id","targetTable":"projects","targetColumn":"id"}]`)
	routes.on(http.MethodGet, projectsA+"/realtime/tables", 200, `[]`)
	cs := session(t, routes, &recordingAudit{}, readOnlyCaller())
	out := structured(t, callTool(t, cs, "get_graphql_schema", map[string]any{"project_id": testProjectA}))
	tables, _ := out["tables"].([]any)
	fields := map[string]any{}
	for _, raw := range tables {
		table, _ := raw.(map[string]any)
		relationships, _ := table["relationships"].([]any)
		if len(relationships) != 1 {
			t.Fatalf("%v relationships = %v", table["name"], table["relationships"])
		}
		fields[table["name"].(string)] = relationships[0].(map[string]any)["field"]
	}
	if fields["tasks"] != "publicProject" || fields["projects"] != "publicTasks" {
		t.Fatalf("fields = %v", fields)
	}
}
