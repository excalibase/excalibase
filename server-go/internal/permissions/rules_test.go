package permissions

import (
	"encoding/json"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/domain"
)

func rule(field string, fieldType domain.FieldType, op domain.RuleOperator, value string) domain.Rule {
	return domain.Rule{Field: field, FieldType: fieldType, Operator: op, Value: value}
}

func expJSON(t *testing.T, exp Exp) string {
	t.Helper()
	out, err := json.Marshal(exp)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(out)
}

func TestConvertRule(t *testing.T) {
	cases := []struct {
		name string
		rule domain.Rule
		want string
	}{
		{"eq string", rule("status", domain.FieldString, domain.OpEQ, "open"), `{"status":{"_eq":"open"}}`},
		{"neq integer", rule("n", domain.FieldInteger, domain.OpNEQ, "5"), `{"n":{"_neq":5}}`},
		{"gt long", rule("n", domain.FieldLong, domain.OpGT, "9007199254740993"), `{"n":{"_gt":9007199254740993}}`},
		{"gte double", rule("price", domain.FieldDouble, domain.OpGTE, "1.5"), `{"price":{"_gte":1.5}}`},
		{"lt decimal", rule("price", "DECIMAL", domain.OpLT, "10.10"), `{"price":{"_lt":10.10}}`},
		{"lte date", rule("d", domain.FieldDate, domain.OpLTE, "2026-01-01"), `{"d":{"_lte":"2026-01-01"}}`},
		{"eq boolean", rule("done", domain.FieldBoolean, domain.OpEQ, "TRUE"), `{"done":{"_eq":true}}`},
		{"eq boolean anything else is false", rule("done", domain.FieldBoolean, domain.OpEQ, "no"), `{"done":{"_eq":false}}`},
		{"eq uuid", rule("id", domain.FieldUUID, domain.OpEQ, "0b0e"), `{"id":{"_eq":"0b0e"}}`},
		{"in integers", rule("n", domain.FieldInteger, domain.OpIN, "1, 2,3"), `{"n":{"_in":[1,2,3]}}`},
		{"not in strings", rule("s", domain.FieldString, domain.OpNOTIN, "a,b"), `{"s":{"_nin":["a","b"]}}`},
		{"like", rule("s", domain.FieldString, domain.OpLIKE, "%x"), `{"s":{"_like":"%x"}}`},
		{"not like", rule("s", domain.FieldString, domain.OpNOTLIKE, "x%"), `{"s":{"_nlike":"x%"}}`},
		{"is null", rule("s", domain.FieldString, domain.OpISNULL, ""), `{"s":{"_is_null":true}}`},
		{"is not null", rule("s", domain.FieldString, domain.OpISNOTNULL, ""), `{"s":{"_is_null":false}}`},
		{"current user", rule("owner_id", domain.FieldUUID, domain.OpEQ, "{{currentUserId}}"), `{"owner_id":{"_eq":"X-Excalibase-User-Id"}}`},
		{"current user padded", rule("owner_id", domain.FieldUUID, domain.OpEQ, "{{ currentUserId }}"), `{"owner_id":{"_eq":"X-Excalibase-User-Id"}}`},
		{"current user in", rule("owner_id", domain.FieldUUID, domain.OpIN, "{{currentUserId}}"), `{"owner_id":{"_eq":"X-Excalibase-User-Id"}}`},
		{"current user not in", rule("owner_id", domain.FieldUUID, domain.OpNOTIN, "{{currentUserId}}"), `{"owner_id":{"_neq":"X-Excalibase-User-Id"}}`},
		{"string literal kept as given", rule("s", domain.FieldString, domain.OpEQ, " a "), `{"s":{"_eq":" a "}}`},
		{"current tenant", rule("project", domain.FieldString, domain.OpEQ, "{{currentTenantId}}"), `{"project":{"_eq":"X-Excalibase-Project-Id"}}`},
		{"custom claim", rule("region", domain.FieldString, domain.OpEQ, "{{Region}}"), `{"region":{"_eq":"X-Excalibase-region"}}`},
		{"custom claim underscore", rule("t", domain.FieldString, domain.OpNEQ, "{{tenant_id}}"), `{"t":{"_neq":"X-Excalibase-tenant_id"}}`},
		{"custom claim in", rule("t", domain.FieldString, domain.OpIN, "{{teams}}"), `{"t":{"_in":"X-Excalibase-teams"}}`},
		{"roles eq", rule("role", domain.FieldString, domain.OpEQ, "{{currentUserRoles}}"), `{"role":{"_eq":"X-Excalibase-Role"}}`},
		{"roles in", rule("role", domain.FieldString, domain.OpIN, "{{currentUserRoles}}"), `{"role":{"_eq":"X-Excalibase-Role"}}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			exp, err := ConvertRule(c.rule)
			if err != nil {
				t.Fatalf("convert: %v", err)
			}
			if got := expJSON(t, exp); got != c.want {
				t.Fatalf("got %s, want %s", got, c.want)
			}
			if err := ValidateBoolExp(json.RawMessage(expJSON(t, exp))); err != nil {
				t.Fatalf("converted rule fails the grammar: %v", err)
			}
		})
	}
}

func TestConvertRule_Unexpressible(t *testing.T) {
	cases := []struct {
		name string
		rule domain.Rule
	}{
		{"json path", rule("meta.owner", domain.FieldString, domain.OpEQ, "x")},
		{"bad column", rule("a b", domain.FieldString, domain.OpEQ, "x")},
		{"now", rule("t", domain.FieldDatetime, domain.OpLT, "{{now}}")},
		{"today", rule("t", domain.FieldDate, domain.OpLT, "{{today}}")},
		{"days ago", rule("t", domain.FieldDatetime, domain.OpGT, "{{daysAgo:7}}")},
		{"groups", rule("g", domain.FieldString, domain.OpIN, "{{currentUserGroupIds}}")},
		{"roles with neq", rule("role", domain.FieldString, domain.OpNEQ, "{{currentUserRoles}}")},
		{"roles with not in", rule("role", domain.FieldString, domain.OpNOTIN, "{{currentUserRoles}}")},
		{"bad claim name", rule("x", domain.FieldString, domain.OpEQ, "{{a.b}}")},
		{"not an integer", rule("n", domain.FieldInteger, domain.OpEQ, "abc")},
		{"not a number in list", rule("n", domain.FieldLong, domain.OpIN, "1,x")},
		{"not a double", rule("n", domain.FieldDouble, domain.OpEQ, "1,5")},
		{"literal reads as a session variable", rule("s", domain.FieldString, domain.OpEQ, "X-Excalibase-User-Id")},
		{"unknown operator", rule("s", domain.FieldString, "BETWEEN", "1")},
		{"unknown field type", rule("s", "BLOB", domain.OpEQ, "1")},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if exp, err := ConvertRule(c.rule); err == nil {
				t.Fatalf("must be unexpressible, got %s", expJSON(t, exp))
			}
		})
	}
}

func TestConvertRules_Logic(t *testing.T) {
	a := rule("a", domain.FieldInteger, domain.OpEQ, "1")
	b := rule("b", domain.FieldInteger, domain.OpEQ, "2")
	cases := []struct {
		name  string
		logic domain.LogicOperator
		rules []domain.Rule
		want  string
	}{
		{"no rules is every row", domain.LogicAnd, nil, `{}`},
		{"single rule", domain.LogicAnd, []domain.Rule{a}, `{"a":{"_eq":1}}`},
		{"and", domain.LogicAnd, []domain.Rule{a, b}, `{"_and":[{"a":{"_eq":1}},{"b":{"_eq":2}}]}`},
		{"empty logic is and", "", []domain.Rule{a, b}, `{"_and":[{"a":{"_eq":1}},{"b":{"_eq":2}}]}`},
		{"or", domain.LogicOr, []domain.Rule{a, b}, `{"_or":[{"a":{"_eq":1}},{"b":{"_eq":2}}]}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			exp, err := ConvertRules(c.logic, c.rules)
			if err != nil {
				t.Fatal(err)
			}
			if got := expJSON(t, exp); got != c.want {
				t.Fatalf("got %s, want %s", got, c.want)
			}
		})
	}
	if _, err := ConvertRules(domain.LogicAnd, []domain.Rule{a, rule("t", domain.FieldDate, domain.OpLT, "{{now}}")}); err == nil {
		t.Fatal("one unexpressible rule makes the policy unexpressible")
	}
	if _, err := ConvertRules("XOR", []domain.Rule{a}); err == nil {
		t.Fatal("unknown logic must be refused")
	}
}

func TestExpCombinators(t *testing.T) {
	a := Exp{"a": map[string]any{"_eq": 1}}
	b := Exp{"b": map[string]any{"_eq": 2}}
	cases := []struct {
		name string
		exp  Exp
		want string
	}{
		{"and of nothing", And(), `{}`},
		{"and drops true", And(Exp{}, a), `{"a":{"_eq":1}}`},
		{"and of two", And(a, b), `{"_and":[{"a":{"_eq":1}},{"b":{"_eq":2}}]}`},
		{"or with true is true", Or(a, Exp{}), `{}`},
		{"or of one", Or(a), `{"a":{"_eq":1}}`},
		{"or of two", Or(a, b), `{"_or":[{"a":{"_eq":1}},{"b":{"_eq":2}}]}`},
		{"not", Not(a), `{"_not":{"a":{"_eq":1}}}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := expJSON(t, c.exp); got != c.want {
				t.Fatalf("got %s, want %s", got, c.want)
			}
		})
	}
}
