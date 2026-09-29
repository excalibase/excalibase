package permissions

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestValidateQualifiedName(t *testing.T) {
	for _, ok := range []string{"public.orders", "sales.order_items", "_x.y$", "a1.b2$"} {
		if err := ValidateQualifiedName(ok); err != nil {
			t.Errorf("%q must be accepted: %v", ok, err)
		}
	}
	for _, bad := range []string{"", "orders", "Public.orders", "public.Orders", "a.b.c", "public.", ".x",
		"public.\"orders\"", "public.or ders", "1a.b", "a.$b", "public." + strings.Repeat("a", 64)} {
		if err := ValidateQualifiedName(bad); err == nil {
			t.Errorf("%q must be refused", bad)
		}
	}
}

func TestValidateRole(t *testing.T) {
	for _, ok := range []string{"anon", "user", "editor", "a", "role_2"} {
		if err := ValidateRole(ok); err != nil {
			t.Errorf("%q must be accepted: %v", ok, err)
		}
	}
	for _, bad := range []string{"", "service", "Editor", "_x", "1a", "a-b", strings.Repeat("a", 64)} {
		if err := ValidateRole(bad); err == nil {
			t.Errorf("%q must be refused", bad)
		}
	}
}

func TestValidateOperation(t *testing.T) {
	for _, ok := range []string{"select", "insert", "update", "delete"} {
		if err := ValidateOperation(ok); err != nil {
			t.Errorf("%q: %v", ok, err)
		}
	}
	for _, bad := range []string{"", "SELECT", "upsert", "all"} {
		if err := ValidateOperation(bad); err == nil {
			t.Errorf("%q must be refused", bad)
		}
	}
}

func TestNormalizePermission_AcceptsEachOperationShape(t *testing.T) {
	cases := []struct{ op, body string }{
		{"select", `{"filter":{"owner_id":{"_eq":"X-Excalibase-User-Id"}},"columns":["id","owner_id"],"limit":100,"allowAggregations":true}`},
		{"select", `{"filter":{},"columns":"*"}`},
		{"select", `{"filter":{},"columns":"*","limit":null}`},
		{"insert", `{"check":{},"columns":"*","set":{"owner_id":"X-Excalibase-User-Id","status":"new","n":1,"flag":true,"meta":{"a":1}}}`},
		{"insert", `{"check":{},"columns":[]}`},
		{"update", `{"filter":{},"check":{"status":{"_neq":"shipped"}},"columns":["status"],"set":{}}`},
		{"update", `{"filter":{},"columns":"*"}`},
		{"delete", `{"filter":{"owner_id":{"_eq":"x-excalibase-user-id"}}}`},
	}
	for _, c := range cases {
		out, err := NormalizePermission(c.op, []byte(c.body))
		if err != nil {
			t.Errorf("%s %s: %v", c.op, c.body, err)
			continue
		}
		if !json.Valid(out) {
			t.Errorf("%s: normalized output is not JSON: %s", c.op, out)
		}
	}
}

func TestNormalizePermission_IsCanonical(t *testing.T) {
	out, err := NormalizePermission("select", []byte(`{ "columns" : "*", "filter" : { } }`))
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != `{"columns":"*","filter":{}}` {
		t.Fatalf("got %s", out)
	}
}

func TestNormalizePermission_RefusesWrongShapes(t *testing.T) {
	cases := []struct{ name, op, body string }{
		{"not an object", "select", `[]`},
		{"not json", "select", `{`},
		{"trailing data", "select", `{"filter":{},"columns":"*"} {}`},
		{"select needs filter", "select", `{"columns":"*"}`},
		{"select needs columns", "select", `{"filter":{}}`},
		{"insert needs check", "insert", `{"columns":"*"}`},
		{"update needs filter", "update", `{"columns":"*"}`},
		{"delete needs filter", "delete", `{}`},
		{"check on select", "select", `{"filter":{},"columns":"*","check":{}}`},
		{"set on select", "select", `{"filter":{},"columns":"*","set":{}}`},
		{"filter on insert", "insert", `{"check":{},"columns":"*","filter":{}}`},
		{"limit on insert", "insert", `{"check":{},"columns":"*","limit":1}`},
		{"columns on delete", "delete", `{"filter":{},"columns":"*"}`},
		{"unknown key", "select", `{"filter":{},"columns":"*","enforced":true}`},
		{"columns not star", "select", `{"filter":{},"columns":"all"}`},
		{"columns bad name", "select", `{"filter":{},"columns":["a b"]}`},
		{"columns duplicate", "select", `{"filter":{},"columns":["a","a"]}`},
		{"columns number", "select", `{"filter":{},"columns":[1]}`},
		{"limit zero", "select", `{"filter":{},"columns":"*","limit":0}`},
		{"limit negative", "select", `{"filter":{},"columns":"*","limit":-1}`},
		{"limit fraction", "select", `{"filter":{},"columns":"*","limit":1.5}`},
		{"limit string", "select", `{"filter":{},"columns":"*","limit":"10"}`},
		{"aggregations not bool", "select", `{"filter":{},"columns":"*","allowAggregations":"yes"}`},
		{"set not object", "insert", `{"check":{},"columns":"*","set":[]}`},
		{"set bad column", "insert", `{"check":{},"columns":"*","set":{"a b":1}}`},
		{"set bad session variable", "insert", `{"check":{},"columns":"*","set":{"a":"X-Excalibase-"}}`},
		{"filter not object", "delete", `{"filter":[]}`},
		{"filter null", "delete", `{"filter":null}`},
		{"bad filter", "delete", `{"filter":{"a":{"_regex":"x"}}}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := NormalizePermission(c.op, []byte(c.body)); err == nil {
				t.Fatalf("%s %s must be refused", c.op, c.body)
			}
		})
	}
}

func TestValidateBoolExp_Accepts(t *testing.T) {
	for _, exp := range []string{
		`{}`,
		`{"a":{"_eq":1}}`,
		`{"a":{"_eq":"x"},"b":{"_neq":true}}`,
		`{"a":{"_gt":1,"_lt":5}}`,
		`{"a":{"_gte":1.5},"b":{"_lte":"2020-01-01"}}`,
		`{"a":{"_in":[1,2,3]}}`,
		`{"a":{"_in":[]}}`,
		`{"a":{"_nin":"X-Excalibase-Allowed-Ids"}}`,
		`{"a":{"_like":"%x%"},"b":{"_nlike":"y%"}}`,
		`{"a":{"_is_null":true}}`,
		`{"_and":[{"a":{"_eq":1}},{"_or":[{"b":{"_eq":2}},{"_not":{"c":{"_eq":3}}}]}]}`,
		`{"_and":[]}`,
		`{"author":{"id":{"_eq":"X-Excalibase-User-Id"}}}`,
		`{"author":{}}`,
		`{"author":{"_and":[{"id":{"_eq":1}}]}}`,
		`{"_exists":{"_table":"public.members","_where":{"user_id":{"_eq":"X-Excalibase-User-Id"}}}}`,
		`{"_id":{"_eq":1}}`,
		`{"orgId":{"_eq":"X-Excalibase-tenant_id"}}`,
	} {
		if err := ValidateBoolExp(json.RawMessage(exp)); err != nil {
			t.Errorf("%s must be accepted: %v", exp, err)
		}
	}
}

func TestValidateBoolExp_Refuses(t *testing.T) {
	for _, exp := range []string{
		`[]`,
		`null`,
		`"x"`,
		`{"a":1}`,
		`{"a":{"_eq":null}}`,
		`{"a":{"_eq":[1]}}`,
		`{"a":{"_eq":{"b":1}}}`,
		`{"a":{"_regex":"x"}}`,
		`{"a":{"_eq":1,"b":{}}}`,
		`{"a":{"_in":1}}`,
		`{"a":{"_in":"literal"}}`,
		`{"a":{"_in":[null]}}`,
		`{"a":{"_in":[[1]]}}`,
		`{"a":{"_in":["X-Excalibase-User-Id"]}}`,
		`{"a":{"_is_null":"yes"}}`,
		`{"a":{"_like":1}}`,
		`{"a":{"_eq":"X-Excalibase-"}}`,
		`{"a":{"_eq":"x-excalibase-bad.name"}}`,
		`{"_and":{}}`,
		`{"_or":[1]}`,
		`{"_not":[]}`,
		`{"_exists":{"_table":"members","_where":{}}}`,
		`{"_exists":{"_table":"public.members"}}`,
		`{"_exists":{"_where":{}}}`,
		`{"_exists":{"_table":"public.members","_where":{},"x":1}}`,
		`{"a b":{"_eq":1}}`,
		`{"":{"_eq":1}}`,
	} {
		if err := ValidateBoolExp(json.RawMessage(exp)); err == nil {
			t.Errorf("%s must be refused", exp)
		}
	}
}

func nested(depth int) string {
	exp := `{}`
	for i := 0; i < depth-1; i++ {
		exp = `{"_not":` + exp + `}`
	}
	return exp
}

func TestValidateBoolExp_DepthLimit(t *testing.T) {
	if err := ValidateBoolExp(json.RawMessage(nested(MaxExpressionDepth))); err != nil {
		t.Fatalf("depth %d must be accepted: %v", MaxExpressionDepth, err)
	}
	if err := ValidateBoolExp(json.RawMessage(nested(MaxExpressionDepth + 1))); err == nil {
		t.Fatalf("depth %d must be refused", MaxExpressionDepth+1)
	}
}

func TestValidateBoolExp_NodeLimit(t *testing.T) {
	terms := func(n int) string {
		parts := make([]string, n)
		for i := range parts {
			parts[i] = `{"a":{"_eq":1}}`
		}
		return `{"_and":[` + strings.Join(parts, ",") + `]}`
	}
	// one node for the root, one per element, one per comparison
	if err := ValidateBoolExp(json.RawMessage(terms(99))); err != nil {
		t.Fatalf("199 nodes must be accepted: %v", err)
	}
	if err := ValidateBoolExp(json.RawMessage(terms(100))); err == nil {
		t.Fatal("201 nodes must be refused")
	}
}

func TestIsSessionVariable(t *testing.T) {
	for _, ok := range []string{"X-Excalibase-User-Id", "x-excalibase-role", "X-EXCALIBASE-tenant_id"} {
		if !IsSessionVariable(ok) {
			t.Errorf("%q must be a session variable", ok)
		}
	}
	for _, bad := range []string{"User-Id", "X-Hasura-User-Id", "X-Excalibase-", "X-Excalibase-a.b"} {
		if IsSessionVariable(bad) {
			t.Errorf("%q must not be a session variable", bad)
		}
	}
}
