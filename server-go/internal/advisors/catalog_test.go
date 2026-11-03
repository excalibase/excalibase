package advisors

import (
	"strings"
	"testing"
)

func TestForeignKeyWithoutIndexFinding(t *testing.T) {
	f := fkFinding(fkRow{Schema: "public", Table: "order items", Constraint: "items_order", Columns: []string{"order_id", `we"ird`}})
	if f.Check != CheckFKWithoutIndex || f.Category != CategoryPerformance || f.Level != LevelWarning {
		t.Fatalf("finding = %+v", f)
	}
	if f.Constraint != "items_order" || f.Object != "public.order items" || strings.Join(f.Columns, ",") != `order_id,we"ird` {
		t.Fatalf("object %q columns %v", f.Object, f.Columns)
	}
	want := `CREATE INDEX CONCURRENTLY ON "public"."order items" ("order_id", "we""ird");`
	if f.FixSQL != want {
		t.Fatalf("fix = %s, want %s", f.FixSQL, want)
	}
	if !strings.Contains(f.Message, "items_order") || !strings.Contains(f.FixHint, "transaction") {
		t.Fatalf("message %q hint %q", f.Message, f.FixHint)
	}
}

// CONCURRENTLY is refused on a partitioned table.
func TestForeignKeyOnPartitionedTableIndexesWithoutConcurrently(t *testing.T) {
	f := fkFinding(fkRow{Schema: "public", Table: "events", Constraint: "events_user", Columns: []string{"user_id"}, Partitioned: true})
	if f.FixSQL != `CREATE INDEX ON "public"."events" ("user_id");` {
		t.Fatalf("fix = %s", f.FixSQL)
	}
}

func TestUnusedIndexFindingPerEngine(t *testing.T) {
	pg := unusedIndexFinding(EnginePostgres, indexRef{Schema: "public", Table: "orders", Index: "orders_note_idx"})
	if pg.Check != CheckUnusedIndex || pg.Level != LevelInfo || pg.Index != "orders_note_idx" {
		t.Fatalf("finding = %+v", pg)
	}
	if pg.FixSQL != `DROP INDEX CONCURRENTLY "public"."orders_note_idx";` {
		t.Fatalf("pg fix = %s", pg.FixSQL)
	}
	my := unusedIndexFinding(EngineMySQL, indexRef{Schema: "app", Table: "orders", Index: "note`idx"})
	if my.FixSQL != "ALTER TABLE `app`.`orders` DROP INDEX `note``idx`;" {
		t.Fatalf("mysql fix = %s", my.FixSQL)
	}
	if !strings.Contains(my.Message, "server started") || !strings.Contains(pg.Message, "statistics were last reset") {
		t.Fatalf("messages %q / %q", pg.Message, my.Message)
	}
}

func TestDuplicateIndexesKeepThePrimaryThenUniqueThenName(t *testing.T) {
	rows := []indexRow{
		{indexRef: indexRef{Schema: "public", Table: "orders", Index: "orders_b"}, Key: "k1"},
		{indexRef: indexRef{Schema: "public", Table: "orders", Index: "orders_uniq"}, Key: "k1", Unique: true},
		{indexRef: indexRef{Schema: "public", Table: "orders", Index: "orders_a"}, Key: "k1"},
		{indexRef: indexRef{Schema: "public", Table: "orders", Index: "orders_pkey"}, Key: "k2", Primary: true, Unique: true, Constraint: true},
		{indexRef: indexRef{Schema: "public", Table: "orders", Index: "orders_id_copy"}, Key: "k2"},
		{indexRef: indexRef{Schema: "public", Table: "orders", Index: "lonely"}, Key: "k3"},
		// The same key on another table is no duplicate.
		{indexRef: indexRef{Schema: "public", Table: "items", Index: "items_a"}, Key: "k1"},
	}
	got := duplicateFindings(EnginePostgres, rows)
	dropped := map[string]Finding{}
	for _, f := range got {
		dropped[f.Index] = f
	}
	if len(got) != 3 || dropped["orders_a"].Check == "" || dropped["orders_b"].Check == "" || dropped["orders_id_copy"].Check == "" {
		t.Fatalf("findings = %+v", got)
	}
	if !strings.Contains(dropped["orders_a"].Message, "orders_uniq") || !strings.Contains(dropped["orders_id_copy"].Message, "orders_pkey") {
		t.Fatalf("keepers: %q / %q", dropped["orders_a"].Message, dropped["orders_id_copy"].Message)
	}
	if dropped["orders_a"].FixSQL != `DROP INDEX CONCURRENTLY "public"."orders_a";` || dropped["orders_a"].Level != LevelWarning {
		t.Fatalf("finding = %+v", dropped["orders_a"])
	}
}

// An index that backs a constraint is dropped with the constraint, never DROP INDEX.
func TestDuplicateBackingAConstraintGetsNoDropIndex(t *testing.T) {
	rows := []indexRow{
		{indexRef: indexRef{Schema: "public", Table: "t", Index: "t_pkey"}, Key: "k", Primary: true, Unique: true, Constraint: true},
		{indexRef: indexRef{Schema: "public", Table: "t", Index: "t_code_key"}, Key: "k", Unique: true, Constraint: true},
	}
	got := duplicateFindings(EnginePostgres, rows)
	if len(got) != 1 || got[0].Index != "t_code_key" || got[0].FixSQL != "" || !strings.Contains(got[0].FixHint, "constraint") {
		t.Fatalf("findings = %+v", got)
	}
}

func TestDuplicateOnMySQL(t *testing.T) {
	rows := []indexRow{
		{indexRef: indexRef{Schema: "app", Table: "orders", Index: "PRIMARY"}, Key: "id", Primary: true, Unique: true},
		{indexRef: indexRef{Schema: "app", Table: "orders", Index: "orders_id"}, Key: "id"},
	}
	got := duplicateFindings(EngineMySQL, rows)
	if len(got) != 1 || got[0].FixSQL != "ALTER TABLE `app`.`orders` DROP INDEX `orders_id`;" {
		t.Fatalf("findings = %+v", got)
	}
}

func TestDuplicateOnPartitionedTableDropsWithoutConcurrently(t *testing.T) {
	rows := []indexRow{
		{indexRef: indexRef{Schema: "public", Table: "events", Index: "events_a", Partitioned: true}, Key: "k"},
		{indexRef: indexRef{Schema: "public", Table: "events", Index: "events_b", Partitioned: true}, Key: "k"},
	}
	got := duplicateFindings(EnginePostgres, rows)
	if len(got) != 1 || got[0].FixSQL != `DROP INDEX "public"."events_b";` {
		t.Fatalf("findings = %+v", got)
	}
}

func TestNoPrimaryKeyFinding(t *testing.T) {
	f := noPrimaryKeyFinding(EnginePostgres, tableRef{Schema: "public", Table: "logs"})
	if f.Check != CheckNoPrimaryKey || f.Level != LevelWarning || f.FixSQL != "" {
		t.Fatalf("finding = %+v", f)
	}
	if !strings.Contains(f.FixHint, `ALTER TABLE "public"."logs" ADD PRIMARY KEY`) {
		t.Fatalf("hint = %s", f.FixHint)
	}
	my := noPrimaryKeyFinding(EngineMySQL, tableRef{Schema: "app", Table: "logs"})
	if !strings.Contains(my.FixHint, "ALTER TABLE `app`.`logs` ADD PRIMARY KEY") {
		t.Fatalf("hint = %s", my.FixHint)
	}
}

func TestLargeTableFinding(t *testing.T) {
	f := largeTableFinding(EnginePostgres, largeTableRow{tableRef: tableRef{Schema: "public", Table: "events"}, Rows: 2_500_000, FullScans: 40, IndexScans: 3})
	if f.Check != CheckLargeTableNoIndexUse || f.Level != LevelWarning || f.FixSQL != "" {
		t.Fatalf("finding = %+v", f)
	}
	if !strings.Contains(f.Message, "2500000") || !strings.Contains(f.Message, "40") || !strings.Contains(f.FixHint, "Performance") {
		t.Fatalf("message %q hint %q", f.Message, f.FixHint)
	}
}

func TestIdentifierQuoting(t *testing.T) {
	if got := pgIdent(`a"b`); got != `"a""b"` {
		t.Errorf("pg = %s", got)
	}
	if got := mysqlIdent("a`b"); got != "`a``b`" {
		t.Errorf("mysql = %s", got)
	}
}

func TestSortPutsErrorsFirst(t *testing.T) {
	findings := []Finding{
		{Check: "b", Level: LevelInfo, Object: "x"},
		{Check: "a", Level: LevelWarning, Object: "z"},
		{Check: "a", Level: LevelError, Object: "y"},
		{Check: "a", Level: LevelWarning, Object: "a"},
	}
	sortFindings(findings)
	var order []string
	for _, f := range findings {
		order = append(order, f.Level+":"+f.Object)
	}
	if strings.Join(order, ",") != "error:y,warning:a,warning:z,info:x" {
		t.Fatalf("order = %v", order)
	}
}
