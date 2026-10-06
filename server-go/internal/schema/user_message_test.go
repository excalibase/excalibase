package schema

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/lib/pq"
)

// A Postgres refusal reaches the SQL editor as Postgres's sentence, without
// the driver's "pq: " prefix, even when our code wrapped it.
func TestUserMessageDropsTheDriverPrefix(t *testing.T) {
	syntax := &pq.Error{Code: "42601", Message: `syntax error at or near "selec"`}
	cases := []struct {
		name string
		err  error
		want string
	}{
		{"bare", syntax, `syntax error at or near "selec"`},
		{"wrapped", fmt.Errorf("commit: %w", syntax), `commit: syntax error at or near "selec"`},
		{"not postgres", errors.New("connection reset"), "connection reset"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := userMessage(tc.err); got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestReadOnlyErrorDropsTheDriverPrefix(t *testing.T) {
	syntax := &pq.Error{Code: "42601", Message: `syntax error at or near "selec"`}
	if got := readOnlyError(syntax); got != syntax.Message {
		t.Errorf("got %q", got)
	}
}

func TestCheckIdentifier(t *testing.T) {
	if err := CheckIdentifier("table", strings.Repeat("a", 63)); err != nil {
		t.Errorf("63 bytes is Postgres's limit and allowed: %v", err)
	}
	if err := CheckIdentifier("table", "my table!"); err != nil {
		t.Errorf("a quoted name stays possible: %v", err)
	}
	var nameErr *InvalidNameError
	long := CheckIdentifier("column", strings.Repeat("a", 64))
	if !errors.As(long, &nameErr) || long.Error() != "Column names can be at most 63 characters; this one has 64" {
		t.Errorf("over 63 bytes refused by name, got %v", long)
	}
	if err := CheckIdentifier("table", strings.Repeat("é", 32)); !errors.As(err, &nameErr) {
		t.Errorf("the limit counts bytes, not characters: %v", err)
	}
	if err := CheckIdentifier("table", ""); !errors.As(err, &nameErr) {
		t.Errorf("empty refused: %v", err)
	}
}

// Every DDL path that names something refuses an over-long name before any
// SQL is built, instead of letting Postgres truncate it.
func TestCreateTableSQLRefusesLongNames(t *testing.T) {
	long := strings.Repeat("x", 64)
	var nameErr *InvalidNameError
	if _, err := buildCreateTableSQL("public", CreateTableRequest{Name: long}); !errors.As(err, &nameErr) {
		t.Errorf("long table name: %v", err)
	}
	req := CreateTableRequest{Name: "t", Columns: []CreateColumnDef{{Name: long, Type: "text", Nullable: true}}}
	if _, err := buildCreateTableSQL("public", req); !errors.As(err, &nameErr) {
		t.Errorf("long column name: %v", err)
	}
}

func TestDDLRefusesLongNamesBeforeTouchingTheDatabase(t *testing.T) {
	long := strings.Repeat("x", 64)
	i := &Introspector{}
	var nameErr *InvalidNameError
	if err := i.AddColumn(t.Context(), nil, "public", "t", AddColumnRequest{Name: long, Type: "text"}); !errors.As(err, &nameErr) {
		t.Errorf("add column: %v", err)
	}
	if err := i.AlterColumn(t.Context(), nil, "public", "t", "c", AlterColumnRequest{NewName: &long}); !errors.As(err, &nameErr) {
		t.Errorf("rename column: %v", err)
	}
	if err := i.UpdateTable(t.Context(), nil, "public", "t", UpdateTableRequest{NewName: &long}); !errors.As(err, &nameErr) {
		t.Errorf("rename table: %v", err)
	}
	if err := i.CreateIndex(t.Context(), nil, CreateIndexRequest{Name: long}); !errors.As(err, &nameErr) {
		t.Errorf("index: %v", err)
	}
	if err := i.CreatePolicy(t.Context(), nil, CreatePolicyRequest{Name: long}); !errors.As(err, &nameErr) {
		t.Errorf("policy: %v", err)
	}
	if err := i.CreateFunction(t.Context(), nil, CreateFunctionRequest{Name: long}); !errors.As(err, &nameErr) {
		t.Errorf("function: %v", err)
	}
	if err := i.CreateTrigger(t.Context(), nil, CreateTriggerRequest{Name: long}); !errors.As(err, &nameErr) {
		t.Errorf("trigger: %v", err)
	}
	if err := i.CreateRole(t.Context(), nil, CreateRoleRequest{Name: long}); !errors.As(err, &nameErr) {
		t.Errorf("role: %v", err)
	}
	if err := i.UpdateTable(t.Context(), nil, "public", "t", UpdateTableRequest{NewSchema: &long}); !errors.As(err, &nameErr) || !strings.HasPrefix(err.Error(), "Schema names") {
		t.Errorf("move schema: %v", err)
	}
}

// An index, trigger or policy may leave its name to Postgres; only a long one is refused.
func TestCheckNameLengthLeavesAnEmptyNameToTheCaller(t *testing.T) {
	if err := checkNameLength("index", ""); err != nil {
		t.Errorf("empty: %v", err)
	}
	if err := checkNameLength("index", "orders_idx"); err != nil {
		t.Errorf("short: %v", err)
	}
	if got := capitalize(""); got != "" {
		t.Errorf("capitalize empty: %q", got)
	}
}

// A database that cannot be reached is reported as the driver said it, with
// the step that failed.
func TestQueriesOnAClosedDatabaseSayWhichStepFailed(t *testing.T) {
	db, err := sql.Open("postgres", "postgres://nobody@127.0.0.1:1/none?sslmode=disable")
	if err != nil {
		t.Fatal(err)
	}
	db.Close()
	i := NewIntrospector()
	if got := i.ExecuteQuery(t.Context(), db, "SELECT 1"); !strings.HasPrefix(got.Error, "begin tx: ") {
		t.Errorf("query: %q", got.Error)
	}
	if got := i.ExecuteReadOnlyQuery(t.Context(), db, "SELECT 1"); !strings.HasPrefix(got.Error, "open connection: ") {
		t.Errorf("read-only query: %q", got.Error)
	}
	if got := i.ExecuteDDL(t.Context(), db, "SELECT 1"); got.Success || got.Error == "" {
		t.Errorf("ddl: %+v", got)
	}
}
