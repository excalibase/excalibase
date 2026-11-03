package advisors

import (
	"fmt"
	"sort"
	"strings"
)

type tableRef struct {
	Schema, Table string
}

func (t tableRef) object() string { return t.Schema + "." + t.Table }

type indexRef struct {
	Schema, Table, Index string
	// Partitioned indexes cannot be dropped CONCURRENTLY.
	Partitioned bool
}

func (i indexRef) object() string { return i.Schema + "." + i.Table }

// fkRow is a Postgres foreign key no index leads with.
type fkRow struct {
	Schema, Table, Constraint string
	Columns                   []string
	Partitioned               bool
}

func fkFinding(row fkRow) Finding {
	quoted := make([]string, len(row.Columns))
	for i, column := range row.Columns {
		quoted[i] = pgIdent(column)
	}
	create := "CREATE INDEX CONCURRENTLY ON "
	hint := "Index the foreign key's columns. CONCURRENTLY builds the index without blocking writes; run it on its own, not inside a transaction or a migration."
	if row.Partitioned {
		create = "CREATE INDEX ON "
		hint = "Index the foreign key's columns; a partitioned table builds the index on every partition."
	}
	return Finding{
		Check: CheckFKWithoutIndex, Category: CategoryPerformance, Level: LevelWarning,
		Object: row.Schema + "." + row.Table, Columns: row.Columns, Constraint: row.Constraint,
		Message: fmt.Sprintf("Foreign key %s on %s.%s (%s) has no index: joins on it and every delete or key update of the referenced row scan the whole table.",
			row.Constraint, row.Schema, row.Table, strings.Join(row.Columns, ", ")),
		FixHint: hint,
		FixSQL:  fmt.Sprintf("%s%s (%s);", create, qualified(EnginePostgres, row.Schema, row.Table), strings.Join(quoted, ", ")),
	}
}

func dropIndexSQL(engine string, index indexRef) string {
	if engine == EngineMySQL {
		return fmt.Sprintf("ALTER TABLE %s DROP INDEX %s;", qualified(engine, index.Schema, index.Table), mysqlIdent(index.Index))
	}
	if index.Partitioned {
		return fmt.Sprintf("DROP INDEX %s;", qualified(engine, index.Schema, index.Index))
	}
	return fmt.Sprintf("DROP INDEX CONCURRENTLY %s;", qualified(engine, index.Schema, index.Index))
}

func unusedIndexFinding(engine string, index indexRef) Finding {
	since := "since the database's statistics were last reset"
	if engine == EngineMySQL {
		since = "since the server started"
	}
	return Finding{
		Check: CheckUnusedIndex, Category: CategoryPerformance, Level: LevelInfo,
		Object: index.object(), Index: index.Index,
		Message: fmt.Sprintf("Index %s on %s has not been used %s, while the table has been read. It slows every write and takes space.",
			index.Index, index.object(), since),
		FixHint: "Drop it if no query needs it. A query that runs rarely (a monthly report) may still: check before dropping.",
		FixSQL:  dropIndexSQL(engine, index),
	}
}

// indexRow is one index and the key that makes two indexes the same.
type indexRow struct {
	indexRef
	Key                         string
	Primary, Unique, Constraint bool
}

// keeperFirst orders the index to keep first: primary, then constraint, then unique, then by name.
func keeperFirst(a, b indexRow) bool {
	for _, rank := range [][2]bool{{a.Primary, b.Primary}, {a.Constraint, b.Constraint}, {a.Unique, b.Unique}} {
		if rank[0] != rank[1] {
			return rank[0]
		}
	}
	return a.Index < b.Index
}

// duplicateFindings reports every index another index of the same table already is.
func duplicateFindings(engine string, rows []indexRow) []Finding {
	groups := map[string][]indexRow{}
	var order []string
	for _, row := range rows {
		key := row.Schema + "\x00" + row.Table + "\x00" + row.Key
		if _, seen := groups[key]; !seen {
			order = append(order, key)
		}
		groups[key] = append(groups[key], row)
	}
	var findings []Finding
	for _, key := range order {
		group := groups[key]
		if len(group) < 2 {
			continue
		}
		sort.SliceStable(group, func(i, j int) bool { return keeperFirst(group[i], group[j]) })
		for _, extra := range group[1:] {
			findings = append(findings, duplicateFinding(engine, group[0], extra))
		}
	}
	return findings
}

func duplicateFinding(engine string, keep, extra indexRow) Finding {
	f := Finding{
		Check: CheckDuplicateIndex, Category: CategoryPerformance, Level: LevelWarning,
		Object: extra.object(), Index: extra.Index,
		Message: fmt.Sprintf("Index %s on %s indexes the same columns as %s: every write updates both.", extra.Index, extra.object(), keep.Index),
	}
	if extra.Constraint || extra.Primary {
		f.FixHint = fmt.Sprintf("%s backs a constraint: drop the constraint instead (ALTER TABLE ... DROP CONSTRAINT) if %s enforces the same rule.", extra.Index, keep.Index)
		return f
	}
	f.FixHint = fmt.Sprintf("Drop %s and keep %s.", extra.Index, keep.Index)
	f.FixSQL = dropIndexSQL(engine, extra.indexRef)
	return f
}

func noPrimaryKeyFinding(engine string, table tableRef) Finding {
	return Finding{
		Check: CheckNoPrimaryKey, Category: CategoryPerformance, Level: LevelWarning,
		Object:  table.object(),
		Message: fmt.Sprintf("Table %s has no primary key: nothing identifies one row, so editing a single row and replicating updates and deletes cannot target it reliably.", table.object()),
		FixHint: fmt.Sprintf("Add a primary key on a column that is unique and never null: ALTER TABLE %s ADD PRIMARY KEY (<column>);",
			qualified(engine, table.Schema, table.Table)),
	}
}

// largeTableRow is a large table read mostly without an index. FullScans is
// an estimate on MySQL: rows read by full scans over the table's row count.
type largeTableRow struct {
	tableRef
	Rows, FullScans, IndexScans int64
}

func largeTableFinding(engine string, row largeTableRow) Finding {
	reads := fmt.Sprintf("%d sequential scans, index builds included, against %d index scans", row.FullScans, row.IndexScans)
	hint := "Find the queries that filter or join this table (Monitoring > Performance lists the slowest) and index the columns they filter on."
	if engine == EngineMySQL {
		reads = fmt.Sprintf("about %d full table scans, more rows read by them than through any index", row.FullScans)
		hint = "Find the queries that filter or join this table, EXPLAIN them in the SQL editor, and index the columns they filter on."
	}
	return Finding{
		Check: CheckLargeTableNoIndexUse, Category: CategoryPerformance, Level: LevelWarning,
		Object:  row.object(),
		Message: fmt.Sprintf("Table %s holds about %d rows and is read mostly by full scans (%s).", row.object(), row.Rows, reads),
		FixHint: hint,
	}
}
