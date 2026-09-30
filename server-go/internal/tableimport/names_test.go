package tableimport

import (
	"strings"
	"testing"
)

func TestColumnNames_SanitisedToLowerSnakeIdentifiers(t *testing.T) {
	got := ColumnNames([]string{"First Name", "E-mail Address", "2024 Sales", "", "Ünïcode", `x"; DROP TABLE t;--`}, true)
	want := []string{"first_name", "e_mail_address", "c_2024_sales", "column_4", "n_code", "x_drop_table_t"}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("column %d = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestColumnNames_CollisionsGetASuffix(t *testing.T) {
	got := ColumnNames([]string{"Name", "name", "NAME", "name_2"}, true)
	want := []string{"name", "name_2", "name_3", "name_2_2"}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("column %d = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestColumnNames_WithoutAHeaderAreNumbered(t *testing.T) {
	got := ColumnNames(make([]string, 3), false)
	if strings.Join(got, ",") != "column_1,column_2,column_3" {
		t.Fatalf("got %v", got)
	}
}

func TestColumnNames_LongNamesFitPostgres(t *testing.T) {
	long := strings.Repeat("a", 200)
	got := ColumnNames([]string{long, long}, true)
	for _, name := range got {
		if len(name) > 63 {
			t.Fatalf("%q is longer than 63 bytes", name)
		}
		if err := ValidateIdentifier(name); err != nil {
			t.Fatal(err)
		}
	}
	if got[0] == got[1] {
		t.Fatal("truncation made two columns collide")
	}
}

func TestValidateIdentifier_RefusesAnythingButLowerSnake(t *testing.T) {
	for _, bad := range []string{"", "Users", "a b", `a"b`, "1abc", "a;b", strings.Repeat("a", 64), "naïve"} {
		if ValidateIdentifier(bad) == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	for _, good := range []string{"users", "_tmp", "order_items_2"} {
		if err := ValidateIdentifier(good); err != nil {
			t.Errorf("%q refused: %v", good, err)
		}
	}
}

func TestValidateTargetSchema_RefusesPlatformAndCatalogSchemas(t *testing.T) {
	for _, bad := range []string{"pg_catalog", "pg_toast", "information_schema", "excalibase", "auth"} {
		if ValidateTargetSchema(bad) == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	if err := ValidateTargetSchema("public"); err != nil {
		t.Fatal(err)
	}
}
