package schema

import (
	"errors"
	"strings"
	"testing"
)

func TestValidateMySQLType_AcceptsWhatTheTableEditorOffers(t *testing.T) {
	for _, columnType := range []string{
		"int", "INT", "bigint unsigned auto_increment", "tinyint(1)", "smallint", "decimal(10,2)", "decimal(10, 2)",
		"double", "float", "varchar(255)", "char(36)", "text", "mediumtext", "longtext", "json", "date",
		"datetime", "datetime(6)", "timestamp", "time", "year", "blob", "varbinary(16)", "boolean", "bit(1)",
		"enum('draft','published')", "set('a','b')", "enum('it''s')", "serial",
	} {
		if err := ValidateMySQLType(columnType); err != nil {
			t.Errorf("%q: %v", columnType, err)
		}
	}
}

func TestValidateMySQLType_RefusesAnythingElse(t *testing.T) {
	for _, columnType := range []string{
		"", "int; DROP TABLE x", "varchar(255) DEFAULT 1", "timestamptz", "jsonb", "uuid", "text[]",
		"enum('a\\')", "int /* x */", "varchar", "int(1) GENERATED ALWAYS AS (1)",
	} {
		if err := ValidateMySQLType(columnType); err == nil {
			t.Errorf("%q was accepted", columnType)
		}
	}
}

func TestValidateMySQLDefault(t *testing.T) {
	for _, expression := range []string{"0", "-1.5", "'draft'", "'it''s'", "NULL", "true", "CURRENT_TIMESTAMP", "current_timestamp(6)", "(UUID())", "(uuid_to_bin(uuid()))", "(CURRENT_DATE)"} {
		if _, err := ValidateMySQLDefault(expression); err != nil {
			t.Errorf("%q: %v", expression, err)
		}
	}
	for _, expression := range []string{"'a\\'", "now()", "(SELECT 1)", "1; DROP TABLE x", "nextval('x')", "gen_random_uuid()"} {
		if _, err := ValidateMySQLDefault(expression); err == nil {
			t.Errorf("%q was accepted", expression)
		}
	}
}

func TestBuildMySQLCreateTable(t *testing.T) {
	draft := "'draft'"
	statement, err := buildMySQLCreateTable(CreateTableRequest{
		Name: "orders", Comment: "customer's orders",
		Columns: []CreateColumnDef{
			{Name: "id", Type: "bigint unsigned auto_increment", PrimaryKey: true},
			{Name: "status", Type: "enum('draft','paid')", Nullable: true, Default: &draft},
			{Name: "code", Type: "varchar(32)", Unique: true},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := "CREATE TABLE `orders` (`id` bigint unsigned auto_increment NOT NULL, " +
		"`status` enum('draft','paid') DEFAULT 'draft', `code` varchar(32) NOT NULL UNIQUE, PRIMARY KEY (`id`)) COMMENT = 'customer''s orders'"
	if statement != want {
		t.Fatalf("got  %s\nwant %s", statement, want)
	}
}

func TestBuildMySQLCreateTable_RefusesWhatMySQLCannotTake(t *testing.T) {
	cases := map[string]CreateTableRequest{
		"no columns":    {Name: "empty"},
		"bad name":      {Name: "Orders", Columns: []CreateColumnDef{{Name: "id", Type: "int"}}},
		"postgres type": {Name: "t", Columns: []CreateColumnDef{{Name: "at", Type: "timestamptz"}}},
	}
	for name, request := range cases {
		if _, err := buildMySQLCreateTable(request); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	_, err := buildMySQLCreateTable(CreateTableRequest{Name: "empty"})
	var input *InputError
	if !errors.As(err, &input) || !strings.Contains(err.Error(), "at least one column") {
		t.Fatalf("no columns: %v", err)
	}
}

func TestQuoteMySQLIdent(t *testing.T) {
	if got := QuoteMySQLIdent("we`ird"); got != "`we``ird`" {
		t.Fatalf("got %s", got)
	}
}

func TestBuildMySQLIndex(t *testing.T) {
	statement, err := buildMySQLIndex(CreateIndexRequest{Name: "orders_code", Table: "orders", Columns: []string{"code", "id"}, Unique: true, Type: "btree"})
	if err != nil || statement != "CREATE UNIQUE INDEX `orders_code` ON `orders` (`code`, `id`) USING BTREE" {
		t.Fatalf("%s %v", statement, err)
	}
	if _, err := buildMySQLIndex(CreateIndexRequest{Name: "g", Table: "orders", Columns: []string{"doc"}, Type: "gin"}); err == nil {
		t.Fatal("a gin index was accepted")
	}
}
