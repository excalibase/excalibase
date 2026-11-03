package schema

import (
	"reflect"
	"testing"
)

// evil is a name that would end the statement if it were not quoted.
const (
	evil       = `x"; DROP TABLE users; --`
	evilQuoted = `"x""; DROP TABLE users; --"`
)

func strPtr(s string) *string { return &s }
func boolPtr(b bool) *bool    { return &b }

func TestBuildAddColumnSQLQuotesTheTable(t *testing.T) {
	got, err := buildAddColumnSQL(evil, evil, AddColumnRequest{Name: "age", Type: "integer", Default: strPtr("0"), Unique: true})
	if err != nil {
		t.Fatal(err)
	}
	want := `ALTER TABLE ` + evilQuoted + `.` + evilQuoted + ` ADD COLUMN "age" integer NOT NULL DEFAULT 0 UNIQUE`
	if got != want {
		t.Fatalf("got  %s\nwant %s", got, want)
	}
}

func TestBuildAddColumnSQLRefusesAnInjectedType(t *testing.T) {
	if _, err := buildAddColumnSQL("public", "t", AddColumnRequest{Name: "c", Type: "int; DROP TABLE users"}); err == nil {
		t.Fatal("an injected type was accepted")
	}
}

func TestBuildAlterColumnSQLQuotesEveryIdentifier(t *testing.T) {
	steps, err := buildAlterColumnSQL(evil, evil, evil, AlterColumnRequest{
		Type: strPtr("text"), Nullable: boolPtr(true), DropDefault: true, NewName: strPtr("renamed"),
	})
	if err != nil {
		t.Fatal(err)
	}
	base := `ALTER TABLE ` + evilQuoted + `.` + evilQuoted
	want := []ddlStep{
		{base + ` ALTER COLUMN ` + evilQuoted + ` TYPE text`, "alter column type"},
		{base + ` ALTER COLUMN ` + evilQuoted + ` DROP NOT NULL`, "alter column nullable"},
		{base + ` ALTER COLUMN ` + evilQuoted + ` DROP DEFAULT`, "drop column default"},
		{base + ` RENAME COLUMN ` + evilQuoted + ` TO "renamed"`, "rename column"},
	}
	if !reflect.DeepEqual(steps, want) {
		t.Fatalf("got  %#v\nwant %#v", steps, want)
	}
}

func TestBuildAlterColumnSQLRefusesAnInjectedDefault(t *testing.T) {
	if _, err := buildAlterColumnSQL("public", "t", "c", AlterColumnRequest{Default: strPtr("0; DROP TABLE users")}); err == nil {
		t.Fatal("an injected default was accepted")
	}
}

func TestBuildDropColumnSQLQuotesEveryIdentifier(t *testing.T) {
	want := `ALTER TABLE ` + evilQuoted + `.` + evilQuoted + ` DROP COLUMN ` + evilQuoted
	if got := buildDropColumnSQL(evil, evil, evil); got != want {
		t.Fatalf("got  %s\nwant %s", got, want)
	}
}

func TestBuildTableCommentSQLQuotesTheComment(t *testing.T) {
	want := `COMMENT ON TABLE "public"."t" IS 'it''s''; DROP TABLE users; --'`
	if got := buildTableCommentSQL("public", "t", `it's'; DROP TABLE users; --`); got != want {
		t.Fatalf("got  %s\nwant %s", got, want)
	}
}

func TestBuildUpdateTableSQLQuotesEveryIdentifier(t *testing.T) {
	steps := buildUpdateTableSQL(evil, evil, UpdateTableRequest{
		RlsEnabled: boolPtr(true), Comment: strPtr("o'k"), NewSchema: strPtr(evil), NewName: strPtr("renamed"),
	})
	fqn := evilQuoted + `.` + evilQuoted
	want := []ddlStep{
		{`ALTER TABLE ` + fqn + ` ENABLE ROW LEVEL SECURITY`, "toggle RLS"},
		{`COMMENT ON TABLE ` + fqn + ` IS 'o''k'`, "set comment"},
		{`ALTER TABLE ` + fqn + ` SET SCHEMA ` + evilQuoted, "move schema"},
		{`ALTER TABLE ` + fqn + ` RENAME TO "renamed"`, "rename table"},
	}
	if !reflect.DeepEqual(steps, want) {
		t.Fatalf("got  %#v\nwant %#v", steps, want)
	}
}

func TestBuildDropTableSQLQuotesEveryIdentifier(t *testing.T) {
	want := `DROP TABLE ` + evilQuoted + `.` + evilQuoted + ` CASCADE`
	if got := buildDropTableSQL(evil, evil, true); got != want {
		t.Fatalf("got  %s\nwant %s", got, want)
	}
	if got := buildDropTableSQL("public", "t", false); got != `DROP TABLE "public"."t"` {
		t.Fatalf("got %s", got)
	}
}

func TestBuildCreatePolicySQLQuotesEveryIdentifier(t *testing.T) {
	got, err := buildCreatePolicySQL(CreatePolicyRequest{
		Name: evil, Schema: evil, Table: evil, Command: "select", Roles: "authenticated", Using: strPtr("true"),
	})
	if err != nil {
		t.Fatal(err)
	}
	want := `CREATE POLICY ` + evilQuoted + ` ON ` + evilQuoted + `.` + evilQuoted +
		` AS RESTRICTIVE FOR SELECT TO "authenticated" USING (true)`
	if got != want {
		t.Fatalf("got  %s\nwant %s", got, want)
	}
}

func TestBuildCreatePolicySQLRefusesAnInjectedCommand(t *testing.T) {
	if _, err := buildCreatePolicySQL(CreatePolicyRequest{Name: "p", Table: "t", Command: "ALL TO public; DROP TABLE users"}); err == nil {
		t.Fatal("an injected command was accepted")
	}
}

func TestBuildDropPolicySQLQuotesEveryIdentifier(t *testing.T) {
	want := `DROP POLICY ` + evilQuoted + ` ON ` + evilQuoted
	if got := buildDropPolicySQL(evil, evil); got != want {
		t.Fatalf("got  %s\nwant %s", got, want)
	}
}

func TestBuildCreateFunctionSQLQuotesNameAndBody(t *testing.T) {
	got, err := buildCreateFunctionSQL(CreateFunctionRequest{
		Name: evil, Schema: evil, Language: "sql", ReturnType: "integer", Args: "a integer",
		Body: `SELECT 1'; DROP TABLE users; --`, Volatility: "stable",
	})
	if err != nil {
		t.Fatal(err)
	}
	want := `CREATE OR REPLACE FUNCTION ` + evilQuoted + `.` + evilQuoted +
		`(a integer) RETURNS integer LANGUAGE sql STABLE AS 'SELECT 1''; DROP TABLE users; --'`
	if got != want {
		t.Fatalf("got  %s\nwant %s", got, want)
	}
}

func TestBuildDropFunctionSQL(t *testing.T) {
	got, err := buildDropFunctionSQL(evil, evil, "integer, text")
	if err != nil {
		t.Fatal(err)
	}
	if want := `DROP FUNCTION ` + evilQuoted + `.` + evilQuoted + `(integer, text)`; got != want {
		t.Fatalf("got  %s\nwant %s", got, want)
	}
	if _, err := buildDropFunctionSQL("public", "f", "int); DROP TABLE users; --"); err == nil {
		t.Fatal("injected argument types were accepted")
	}
}

func TestBuildCreateIndexSQLQuotesEveryIdentifier(t *testing.T) {
	got, err := buildCreateIndexSQL(CreateIndexRequest{Name: evil, Schema: evil, Table: evil, Columns: []string{evil, "b"}, Unique: true})
	if err != nil {
		t.Fatal(err)
	}
	want := `CREATE UNIQUE INDEX ` + evilQuoted + ` ON ` + evilQuoted + `.` + evilQuoted + ` USING btree (` + evilQuoted + `, "b")`
	if got != want {
		t.Fatalf("got  %s\nwant %s", got, want)
	}
	if _, err := buildCreateIndexSQL(CreateIndexRequest{Name: "i", Table: "t", Columns: []string{"c"}, Type: "btree (c); DROP TABLE users; --"}); err == nil {
		t.Fatal("an injected index type was accepted")
	}
}

func TestBuildDropIndexSQLQuotesEveryIdentifier(t *testing.T) {
	if got, want := buildDropIndexSQL(evil, evil), `DROP INDEX `+evilQuoted+`.`+evilQuoted; got != want {
		t.Fatalf("got  %s\nwant %s", got, want)
	}
}

func TestBuildCreateTriggerSQLQuotesEveryIdentifier(t *testing.T) {
	got, err := buildCreateTriggerSQL(CreateTriggerRequest{
		Name: evil, Schema: evil, Table: evil, Event: "insert", Timing: "before", ForEachRow: true, Function: evil,
	})
	if err != nil {
		t.Fatal(err)
	}
	want := `CREATE TRIGGER ` + evilQuoted + ` BEFORE INSERT ON ` + evilQuoted + `.` + evilQuoted +
		` FOR EACH ROW EXECUTE FUNCTION ` + evilQuoted + `.` + evilQuoted + `()`
	if got != want {
		t.Fatalf("got  %s\nwant %s", got, want)
	}
	if _, err := buildCreateTriggerSQL(CreateTriggerRequest{Name: "t", Table: "t", Event: "INSERT OR DELETE; --", Timing: "BEFORE", Function: "f"}); err == nil {
		t.Fatal("an injected event was accepted")
	}
}

func TestBuildDropTriggerSQLQuotesEveryIdentifier(t *testing.T) {
	want := `DROP TRIGGER ` + evilQuoted + ` ON ` + evilQuoted + `.` + evilQuoted
	if got := buildDropTriggerSQL(evil, evil, evil); got != want {
		t.Fatalf("got  %s\nwant %s", got, want)
	}
}

func TestBuildUpdateQueryBindsEveryValue(t *testing.T) {
	query, args, err := buildUpdateQuery(evil, evil, evil, "1'; DROP TABLE users; --", map[string]interface{}{
		evil: "v'; DROP TABLE users; --",
		"b":  float64(2),
	})
	if err != nil {
		t.Fatal(err)
	}
	want := `UPDATE ` + evilQuoted + `.` + evilQuoted + ` SET "b" = $1, ` + evilQuoted + ` = $2 WHERE ` + evilQuoted + ` = $3`
	if query != want {
		t.Fatalf("got  %s\nwant %s", query, want)
	}
	if len(args) != 3 || args[1] != "v'; DROP TABLE users; --" || args[2] != "1'; DROP TABLE users; --" {
		t.Fatalf("values were not passed as bind arguments: %#v", args)
	}
}

func TestBuildDeleteQueryBindsTheKey(t *testing.T) {
	want := `DELETE FROM ` + evilQuoted + `.` + evilQuoted + ` WHERE ` + evilQuoted + ` = $1`
	if got := buildDeleteQuery(evil, evil, evil); got != want {
		t.Fatalf("got  %s\nwant %s", got, want)
	}
}
