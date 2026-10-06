package schema

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestCheckNewNameAcceptsPlainLowercaseIdentifiers(t *testing.T) {
	for _, name := range []string{"orders", "_tmp", "order_items_2", strings.Repeat("a", 63)} {
		if err := CheckNewName("table", name); err != nil {
			t.Errorf("CheckNewName(%q) = %v, want nil", name, err)
		}
	}
}

func TestCheckNewNameRefusesNamesThatNeedQuoting(t *testing.T) {
	for _, name := range []string{"", "Orders", "my table", " orders", "2nd", "order-items", "café", strings.Repeat("a", 64)} {
		if err := CheckNewName("table", name); !isInvalidName(err) {
			t.Errorf("CheckNewName(%q) = %v, want an InvalidNameError", name, err)
		}
	}
	err := CheckNewName("column", "First Name")
	want := `Column name "First Name" is not allowed. ` + NameRule
	if err == nil || err.Error() != want {
		t.Errorf("message = %v, want %q", err, want)
	}
}

func TestCreateTableRefusesMixedCaseTableAndColumnNames(t *testing.T) {
	if _, err := buildCreateTableSQL("public", CreateTableRequest{Name: "My Table"}); !isInvalidName(err) {
		t.Errorf("table: err = %v, want InvalidNameError", err)
	}
	req := CreateTableRequest{Name: "people", Columns: []CreateColumnDef{{Name: "First Name", Type: "text"}}}
	if _, err := buildCreateTableSQL("public", req); !isInvalidName(err) {
		t.Errorf("column: err = %v, want InvalidNameError", err)
	}
}

func TestCreateTableRefusesAnInvalidSchema(t *testing.T) {
	err := NewIntrospector().CreateTable(t.Context(), nil, CreateTableRequest{Name: "people", Schema: "Bad Schema"})
	if !isInvalidName(err) {
		t.Errorf("err = %v, want InvalidNameError", err)
	}
}

func TestRenamesAndNewColumnsFollowTheNameRule(t *testing.T) {
	in := NewIntrospector()
	bad := "Bad Name"
	if err := in.UpdateTable(t.Context(), nil, "public", "people", UpdateTableRequest{NewName: &bad}); !isInvalidName(err) {
		t.Errorf("rename table: err = %v", err)
	}
	if err := in.AddColumn(t.Context(), nil, "public", "people", AddColumnRequest{Name: bad, Type: "text"}); !isInvalidName(err) {
		t.Errorf("add column: err = %v", err)
	}
	if err := in.AlterColumn(t.Context(), nil, "public", "people", "name", AlterColumnRequest{NewName: &bad}); !isInvalidName(err) {
		t.Errorf("rename column: err = %v", err)
	}
}

// Our own refusals of a type, default or policy are the caller's mistake.
func TestValidatorsReturnInputErrors(t *testing.T) {
	_, defaultErr := ValidateDefaultExpression("x; drop")
	cases := map[string]error{
		"type":    ValidateTypeName("text; drop"),
		"default": defaultErr,
		"policy":  ValidatePolicyCommand("NUKE"),
	}
	for name, err := range cases {
		var inputErr *InputError
		if !errors.As(err, &inputErr) {
			t.Errorf("%s: err = %v, want an InputError", name, err)
		}
	}
	req := CreateTableRequest{Name: "people", Columns: []CreateColumnDef{{Name: "a", Type: "text; drop"}}}
	_, err := buildCreateTableSQL("public", req)
	var inputErr *InputError
	if !errors.As(err, &inputErr) {
		t.Errorf("wrapped: err = %v, want an InputError", err)
	}
}

func TestInsertWithNoValuesUsesDefaults(t *testing.T) {
	query, args, err := buildInsertQuery("public", "people", map[string]interface{}{})
	if err != nil {
		t.Fatal(err)
	}
	if query != `INSERT INTO "public"."people" DEFAULT VALUES RETURNING *` || len(args) != 0 {
		t.Errorf("query = %q args = %v", query, args)
	}
}

func TestRowValuesKeepNumbersExactAndSendObjectsAsJSON(t *testing.T) {
	data := map[string]interface{}{
		"big":   json.Number("9007199254740993"),
		"price": json.Number("12.345678901234567890"),
		"doc":   map[string]interface{}{"a": json.Number("1")},
		"tags":  []interface{}{"x", "y"},
		"name":  "ann",
		"gone":  nil,
	}
	_, args, err := buildInsertQuery("public", "people", data)
	if err != nil {
		t.Fatal(err)
	}
	// keys sort: big, doc, gone, name, price, tags
	want := []interface{}{"9007199254740993", `{"a":1}`, nil, "ann", "12.345678901234567890", `["x","y"]`}
	for i, expected := range want {
		if args[i] != expected {
			t.Errorf("arg %d = %#v, want %#v", i, args[i], expected)
		}
	}
}

func isInvalidName(err error) bool {
	var nameErr *InvalidNameError
	return errors.As(err, &nameErr)
}
