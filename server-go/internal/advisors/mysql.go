package advisors

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/go-sql-driver/mysql"
)

// A MySQL project is one database; DATABASE() names it, so the server's own
// schemas and the platform's auth and excalibase databases are never read.
const (
	mysqlNoPrimaryKeyQuery = `
SELECT t.table_name FROM information_schema.tables t
WHERE t.table_schema = ? AND t.table_type = 'BASE TABLE'
AND NOT EXISTS (SELECT 1 FROM information_schema.table_constraints c
  WHERE c.table_schema = t.table_schema AND c.table_name = t.table_name AND c.constraint_type = 'PRIMARY KEY')
ORDER BY 1`

	mysqlIndexesQuery = `
SELECT table_name, index_name, non_unique, COALESCE(column_name, CONCAT('(', expression, ')')),
       CONCAT(COALESCE(sub_part, ''), ' ', COALESCE(collation, '')), index_type
FROM information_schema.statistics WHERE table_schema = ?
ORDER BY table_name, index_name, seq_in_index`

	mysqlForeignKeysQuery = `
SELECT table_name, constraint_name, column_name FROM information_schema.key_column_usage
WHERE table_schema = ? AND referenced_table_name IS NOT NULL
ORDER BY table_name, constraint_name, ordinal_position`

	mysqlIndexUseQuery = `
SELECT object_name, COALESCE(index_name, ''), count_star, count_read
FROM performance_schema.table_io_waits_summary_by_index_usage
WHERE object_schema = ? AND object_type = 'TABLE'`

	mysqlTableRowsQuery = `
SELECT table_name, COALESCE(table_rows, 0) FROM information_schema.tables
WHERE table_schema = ? AND table_type = 'BASE TABLE'`
)

const (
	reasonInnoDBIndexesForeignKeys = "InnoDB indexes every foreign key itself: one cannot exist without an index."
	reasonPerformanceSchemaOff     = "performance_schema is off on this server; MySQL keeps index use there."
	reasonPerformanceSchemaDenied  = "the project's admin login may not read performance_schema, where MySQL keeps index use."
)

type mysqlIndex struct {
	Table, Name string
	Columns     []string
	// Parts are each column's prefix length and order, part of what makes two indexes the same.
	Parts  []string
	Unique bool
	Type   string
}

// indexUse is one performance_schema row: Index "" is the table's reads without an index.
type indexUse struct {
	Table, Index string
	Stars, Reads int64
}

func runMySQL(ctx context.Context, tx *sql.Tx, opts Options) ([]Finding, []CheckStatus, error) {
	var database sql.NullString
	if err := tx.QueryRowContext(ctx, "SELECT DATABASE()").Scan(&database); err != nil {
		return nil, nil, fmt.Errorf("read database: %w", err)
	}
	if !database.Valid {
		return nil, nil, errors.New("the connection has no database")
	}
	db := database.String
	findings, err := mysqlNoPrimaryKey(ctx, tx, db)
	if err != nil {
		return nil, nil, fmt.Errorf("%s: %w", CheckNoPrimaryKey, err)
	}
	indexes, err := mysqlIndexes(ctx, tx, db)
	if err != nil {
		return nil, nil, fmt.Errorf("%s: %w", CheckDuplicateIndex, err)
	}
	findings = append(findings, duplicateFindings(EngineMySQL, mysqlDuplicateRows(db, indexes))...)
	foreignKeys, err := mysqlForeignKeys(ctx, tx, db)
	if err != nil {
		return nil, nil, fmt.Errorf("read foreign keys: %w", err)
	}
	statuses := []CheckStatus{
		{ID: CheckFKWithoutIndex, Category: CategoryPerformance, Status: StatusNotApplicable, Reason: reasonInnoDBIndexesForeignKeys},
		ran(CheckDuplicateIndex), ran(CheckNoPrimaryKey),
	}
	usage, reason, err := mysqlIndexUsage(ctx, tx, db)
	if err != nil {
		return nil, nil, err
	}
	if reason != "" {
		for _, id := range []string{CheckUnusedIndex, CheckLargeTableNoIndexUse} {
			statuses = append(statuses, CheckStatus{ID: id, Category: CategoryPerformance, Status: StatusUnavailable, Reason: reason})
		}
		return findings, statuses, nil
	}
	rows, err := mysqlTableRows(ctx, tx, db)
	if err != nil {
		return nil, nil, fmt.Errorf("%s: %w", CheckLargeTableNoIndexUse, err)
	}
	findings = append(findings, mysqlUnusedIndexes(db, indexes, foreignKeys, usage)...)
	findings = append(findings, mysqlLargeTables(db, rows, usage, opts.LargeTableRows)...)
	return findings, append(statuses, ran(CheckUnusedIndex), ran(CheckLargeTableNoIndexUse)), nil
}

func mysqlNoPrimaryKey(ctx context.Context, tx *sql.Tx, db string) ([]Finding, error) {
	var findings []Finding
	err := each(ctx, tx, mysqlNoPrimaryKeyQuery, []any{db}, func(rows *sql.Rows) error {
		table := tableRef{Schema: db}
		if err := rows.Scan(&table.Table); err != nil {
			return err
		}
		findings = append(findings, noPrimaryKeyFinding(EngineMySQL, table))
		return nil
	})
	return findings, err
}

func mysqlIndexes(ctx context.Context, tx *sql.Tx, db string) ([]mysqlIndex, error) {
	var indexes []mysqlIndex
	err := each(ctx, tx, mysqlIndexesQuery, []any{db}, func(rows *sql.Rows) error {
		var table, name, column, part, kind string
		var nonUnique bool
		if err := rows.Scan(&table, &name, &nonUnique, &column, &part, &kind); err != nil {
			return err
		}
		if n := len(indexes); n == 0 || indexes[n-1].Table != table || indexes[n-1].Name != name {
			indexes = append(indexes, mysqlIndex{Table: table, Name: name, Unique: !nonUnique, Type: kind})
		}
		last := &indexes[len(indexes)-1]
		last.Columns = append(last.Columns, column)
		last.Parts = append(last.Parts, part)
		return nil
	})
	return indexes, err
}

func mysqlForeignKeys(ctx context.Context, tx *sql.Tx, db string) (map[string][][]string, error) {
	keys := map[string][][]string{}
	lastTable, lastConstraint := "", ""
	err := each(ctx, tx, mysqlForeignKeysQuery, []any{db}, func(rows *sql.Rows) error {
		var table, constraint, column string
		if err := rows.Scan(&table, &constraint, &column); err != nil {
			return err
		}
		if table != lastTable || constraint != lastConstraint {
			keys[table] = append(keys[table], nil)
			lastTable, lastConstraint = table, constraint
		}
		current := keys[table]
		current[len(current)-1] = append(current[len(current)-1], column)
		return nil
	})
	return keys, err
}

// mysqlIndexUsage reads index use, or why it cannot: performance_schema off
// or not granted is a reason, not a failure.
func mysqlIndexUsage(ctx context.Context, tx *sql.Tx, db string) ([]indexUse, string, error) {
	var enabled bool
	if err := tx.QueryRowContext(ctx, "SELECT @@performance_schema").Scan(&enabled); err != nil {
		return nil, "", fmt.Errorf("read performance_schema: %w", err)
	}
	if !enabled {
		return nil, reasonPerformanceSchemaOff, nil
	}
	var usage []indexUse
	err := each(ctx, tx, mysqlIndexUseQuery, []any{db}, func(rows *sql.Rows) error {
		var use indexUse
		if err := rows.Scan(&use.Table, &use.Index, &use.Stars, &use.Reads); err != nil {
			return err
		}
		usage = append(usage, use)
		return nil
	})
	if accessDenied(err) {
		return nil, reasonPerformanceSchemaDenied, nil
	}
	if err != nil {
		return nil, "", fmt.Errorf("read index use: %w", err)
	}
	return usage, "", nil
}

func mysqlTableRows(ctx context.Context, tx *sql.Tx, db string) (map[string]int64, error) {
	counts := map[string]int64{}
	err := each(ctx, tx, mysqlTableRowsQuery, []any{db}, func(rows *sql.Rows) error {
		var table string
		var count int64
		if err := rows.Scan(&table, &count); err != nil {
			return err
		}
		counts[table] = count
		return nil
	})
	return counts, err
}

// accessDenied is MySQL refusing the login a table or database.
func accessDenied(err error) bool {
	var mysqlErr *mysql.MySQLError
	if !errors.As(err, &mysqlErr) {
		return false
	}
	switch mysqlErr.Number {
	case 1044, 1142, 1143, 1227:
		return true
	}
	return false
}

func mysqlDuplicateRows(db string, indexes []mysqlIndex) []indexRow {
	rows := make([]indexRow, 0, len(indexes))
	for _, index := range indexes {
		key := index.Type + "|" + strings.Join(index.Columns, ",") + "|" + strings.Join(index.Parts, ",")
		rows = append(rows, indexRow{
			indexRef: indexRef{Schema: db, Table: index.Table, Index: index.Name},
			Key:      key, Primary: index.Name == "PRIMARY", Unique: index.Unique,
		})
	}
	return rows
}

// mysqlUnusedIndexes: never used while its table was read, not unique, and not
// the index a foreign key leads with (MySQL refuses to drop that one).
func mysqlUnusedIndexes(db string, indexes []mysqlIndex, foreignKeys map[string][][]string, usage []indexUse) []Finding {
	tableReads := map[string]int64{}
	used := map[[2]string]bool{}
	seen := map[[2]string]bool{}
	for _, use := range usage {
		tableReads[use.Table] += use.Reads
		if use.Index != "" {
			seen[[2]string{use.Table, use.Index}] = true
			used[[2]string{use.Table, use.Index}] = use.Stars > 0
		}
	}
	var findings []Finding
	for _, index := range indexes {
		key := [2]string{index.Table, index.Name}
		if index.Unique || !seen[key] || used[key] || tableReads[index.Table] == 0 || backsForeignKey(index.Columns, foreignKeys[index.Table]) {
			continue
		}
		findings = append(findings, unusedIndexFinding(EngineMySQL, indexRef{Schema: db, Table: index.Table, Index: index.Name}))
	}
	return findings
}

func backsForeignKey(columns []string, foreignKeys [][]string) bool {
	for _, key := range foreignKeys {
		if len(columns) >= len(key) && sameSet(columns[:len(key)], key) {
			return true
		}
	}
	return false
}

func sameSet(a, b []string) bool {
	set := map[string]bool{}
	for _, value := range a {
		set[value] = true
	}
	for _, value := range b {
		if !set[value] {
			return false
		}
	}
	return len(a) == len(b)
}

// mysqlLargeTables: a large table that full scans have read at least once
// over, and more than its indexes have. MySQL counts rows, not scans.
func mysqlLargeTables(db string, tableRows map[string]int64, usage []indexUse, threshold int64) []Finding {
	fullRows, indexRows := map[string]int64{}, map[string]int64{}
	for _, use := range usage {
		if use.Index == "" {
			fullRows[use.Table] += use.Reads
		} else {
			indexRows[use.Table] += use.Reads
		}
	}
	var findings []Finding
	for table, count := range tableRows {
		full := fullRows[table]
		if count < threshold || count == 0 || full < count || full <= indexRows[table] {
			continue
		}
		findings = append(findings, largeTableFinding(EngineMySQL, largeTableRow{
			tableRef: tableRef{Schema: db, Table: table}, Rows: count, FullScans: full / count,
		}))
	}
	return findings
}
