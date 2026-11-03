package graphqlnames

import (
	"reflect"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/schema"
)

func key(constraint, source, column, target string) schema.RelationshipInfo {
	return schema.RelationshipInfo{ConstraintName: constraint, SourceTable: source, SourceColumn: column, TargetTable: target}
}

func TestRelationshipsAsTheEngineNamesThem(t *testing.T) {
	got := Relationships("app", map[string][]string{"orders": {"id"}, "items": {"id", "order_id"}},
		[]schema.RelationshipInfo{key("items_order", "items", "order_id", "orders"), key("x", "items", "y", "elsewhere")})
	want := map[string][]Relationship{
		"items":  {{Field: "appOrder", Kind: "object", Table: "orders", Columns: []string{"order_id"}, DeprecatedName: "appOrderId"}},
		"orders": {{Field: "appItems", Kind: "array", Table: "items", Columns: []string{"order_id"}}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v", got)
	}
}

func TestRelationshipsNameSeveralKeysToOneTableByKey(t *testing.T) {
	got := Relationships("public", map[string][]string{"matches": {"id"}, "teams": {"id"}}, []schema.RelationshipInfo{
		key("home", "matches", "home_team_id", "teams"), key("away", "matches", "awayTeamId", "teams"),
		key("pair", "matches", "a", "teams"), key("pair", "matches", "b", "teams"),
	})
	fields := map[string]bool{}
	for _, field := range got["teams"] {
		fields[field.Field] = true
	}
	for _, want := range []string{"publicMatchesByHomeTeam", "publicMatchesByAwayTeam", "publicMatchesAB"} {
		if !fields[want] {
			t.Errorf("missing %s in %+v", want, got["teams"])
		}
	}
	if got["matches"][0].Field != "publicAwayTeam" {
		t.Errorf("matches = %+v", got["matches"])
	}
}

func TestNames(t *testing.T) {
	if LowerCamel("_Live_poll") != "lIvePoll" || Pascal("live_poll") != "LivePoll" {
		t.Fatal(LowerCamel("_Live_poll"), Pascal("live_poll"))
	}
}
