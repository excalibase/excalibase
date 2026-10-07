package mcpserver

import (
	"reflect"
	"testing"
)

// The expected names are the engine's own (excalibase-graphql NamingUtils and
// GraphqlConstants): a schema-qualified key is always schema-prefixed.
func TestGraphQLFieldsFollowTheEngineNaming(t *testing.T) {
	got := graphQLFields("public", "kanban_cards", false)
	want := rootFields{
		Type: "PublicKanbanCards", Query: "publicKanbanCards", Connection: "publicKanbanCardsConnection",
		Aggregate: "publicKanbanCardsAggregate", Subscription: "publicKanbanCardsChanges",
		Insert: "createPublicKanbanCards", InsertMany: "createManyPublicKanbanCards",
		Update: "updatePublicKanbanCards", Delete: "deletePublicKanbanCards",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v\nwant %+v", got, want)
	}
	view := graphQLFields("my_app", "active_users", true)
	if view.Query != "myAppActiveUsers" || view.Type != "MyAppActiveUsers" || view.Insert != "" || view.Update != "" || view.Delete != "" {
		t.Fatalf("a view is read-only: %+v", view)
	}
}

func TestEngineCaseRulesMatchNamingUtils(t *testing.T) {
	for in, want := range map[string]string{"order_items": "orderItems", "Todos": "todos", "_ab": "aB", "userID": "userID", "a__b": "aB"} {
		if got := lowerCamel(in); got != want {
			t.Errorf("lowerCamel(%q) = %q, want %q", in, got, want)
		}
	}
	for in, want := range map[string]string{"order_items": "OrderItems", "todos": "Todos", "userID": "UserID", "_x": "X"} {
		if got := pascal(in); got != want {
			t.Errorf("pascal(%q) = %q, want %q", in, got, want)
		}
	}
	if got := functionField("public.short_code"); got != "publicShortCode" {
		t.Errorf("functionField = %q", got)
	}
}
