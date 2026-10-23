package advisors

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/lib/pq"
)

// projectRelation keeps the project's own relations: no system or platform
// schema, and nothing an extension created.
const projectRelation = `
n.nspname NOT IN ('excalibase', 'excalibase_cdc', 'auth', 'extensions', 'pg_catalog', 'information_schema')
AND n.nspname NOT LIKE 'pg\_%' AND n.nspname NOT LIKE 'documentdb\_%'
AND NOT EXISTS (SELECT 1 FROM pg_depend d WHERE d.classid = 'pg_class'::regclass AND d.deptype = 'e' AND d.objid = `

func projectRelationOf(oid string) string { return projectRelation + oid + ")" }

// indexLeadsWith is true when index i's leading columns are exactly the set in columns.
const indexLeadsWith = `
(string_to_array(i.indkey::text, ' ')::int2[])[1:cardinality(%[1]s)] @> %[1]s
AND (string_to_array(i.indkey::text, ' ')::int2[])[1:cardinality(%[1]s)] <@ %[1]s`

var pgForeignKeysQuery = `
SELECT n.nspname, t.relname, c.conname, t.relkind = 'p', array_agg(a.attname ORDER BY k.position)
FROM pg_constraint c
JOIN pg_class t ON t.oid = c.conrelid
JOIN pg_namespace n ON n.oid = t.relnamespace
CROSS JOIN LATERAL unnest(c.conkey) WITH ORDINALITY AS k(attnum, position)
JOIN pg_attribute a ON a.attrelid = c.conrelid AND a.attnum = k.attnum
WHERE c.contype = 'f' AND t.relkind IN ('r', 'p') AND NOT t.relispartition AND ` + projectRelationOf("t.oid") + `
AND NOT EXISTS (SELECT 1 FROM pg_index i WHERE i.indrelid = c.conrelid AND i.indpred IS NULL AND i.indisvalid AND ` +
	fmt.Sprintf(indexLeadsWith, "c.conkey") + `)
GROUP BY n.nspname, t.relname, c.conname, t.relkind
ORDER BY 1, 2, 3`

// Every index build scans its table and counts as a sequential scan; a
// concurrent build counts two. Sequential scans past that allowance are reads.
const buildScans = `2 * (SELECT count(*) FROM pg_index b WHERE b.indrelid = t.oid)`

// estimatedRows prefers the planner's estimate: n_live_tup double-counts when
// ANALYZE runs before the inserting session's counters are flushed.
const estimatedRows = `CASE WHEN t.reltuples >= 0 THEN t.reltuples::bigint ELSE st.n_live_tup END`

// pgUnusedIndexesQuery: never scanned while the table was read, not enforcing
// uniqueness or a constraint, and not the index a foreign key relies on.
var pgUnusedIndexesQuery = `
SELECT n.nspname, t.relname, ic.relname
FROM pg_index i
JOIN pg_class ic ON ic.oid = i.indexrelid
JOIN pg_class t ON t.oid = i.indrelid
JOIN pg_namespace n ON n.oid = t.relnamespace
JOIN pg_stat_user_indexes si ON si.indexrelid = i.indexrelid
JOIN pg_stat_user_tables st ON st.relid = i.indrelid
WHERE si.idx_scan = 0 AND NOT i.indisunique AND NOT i.indisprimary
AND (COALESCE(st.idx_scan, 0) > 0 OR st.seq_scan > ` + buildScans + `)
AND NOT EXISTS (SELECT 1 FROM pg_constraint x WHERE x.conindid = i.indexrelid)
AND NOT EXISTS (SELECT 1 FROM pg_constraint c WHERE c.contype = 'f' AND c.conrelid = i.indrelid AND i.indisvalid AND ` +
	fmt.Sprintf(indexLeadsWith, "c.conkey") + `)
AND ` + projectRelationOf("t.oid") + `
ORDER BY 1, 2, 3`

// pgIndexesQuery reads every index with the definition two duplicates share.
var pgIndexesQuery = `
SELECT n.nspname, t.relname, ic.relname,
       concat_ws('|', ic.relam::text, i.indkey::text, i.indclass::text, i.indcollation::text, i.indoption::text,
                 COALESCE(pg_get_expr(i.indexprs, i.indrelid), ''), COALESCE(pg_get_expr(i.indpred, i.indrelid), '')),
       i.indisprimary, i.indisunique,
       EXISTS (SELECT 1 FROM pg_constraint x WHERE x.conindid = i.indexrelid AND x.conrelid = i.indrelid),
       t.relkind = 'p'
FROM pg_index i
JOIN pg_class ic ON ic.oid = i.indexrelid
JOIN pg_class t ON t.oid = i.indrelid
JOIN pg_namespace n ON n.oid = t.relnamespace
WHERE t.relkind IN ('r', 'p', 'm') AND NOT t.relispartition AND ` + projectRelationOf("t.oid") + `
ORDER BY 1, 2, 3`

var pgNoPrimaryKeyQuery = `
SELECT n.nspname, t.relname
FROM pg_class t
JOIN pg_namespace n ON n.oid = t.relnamespace
WHERE t.relkind IN ('r', 'p') AND NOT t.relispartition AND ` + projectRelationOf("t.oid") + `
AND NOT EXISTS (SELECT 1 FROM pg_index i WHERE i.indrelid = t.oid AND i.indisprimary)
ORDER BY 1, 2`

var pgLargeTablesQuery = `
SELECT n.nspname, t.relname, ` + estimatedRows + `, st.seq_scan, COALESCE(st.idx_scan, 0)
FROM pg_stat_user_tables st
JOIN pg_class t ON t.oid = st.relid
JOIN pg_namespace n ON n.oid = t.relnamespace
WHERE ` + estimatedRows + ` >= $1 AND st.seq_scan - ` + buildScans + ` > COALESCE(st.idx_scan, 0)
AND ` + projectRelationOf("t.oid") + `
ORDER BY 1, 2`

func runPostgres(ctx context.Context, tx *sql.Tx, opts Options) ([]Finding, []CheckStatus, error) {
	var findings []Finding
	steps := []struct {
		id  string
		run func() ([]Finding, error)
	}{
		{CheckFKWithoutIndex, func() ([]Finding, error) { return pgForeignKeys(ctx, tx) }},
		{CheckUnusedIndex, func() ([]Finding, error) { return pgUnusedIndexes(ctx, tx) }},
		{CheckDuplicateIndex, func() ([]Finding, error) { return pgDuplicates(ctx, tx) }},
		{CheckNoPrimaryKey, func() ([]Finding, error) { return pgNoPrimaryKey(ctx, tx) }},
		{CheckLargeTableNoIndexUse, func() ([]Finding, error) { return pgLargeTables(ctx, tx, opts.LargeTableRows) }},
	}
	statuses := make([]CheckStatus, 0, len(steps))
	for _, step := range steps {
		found, err := step.run()
		if err != nil {
			return nil, nil, fmt.Errorf("%s: %w", step.id, err)
		}
		findings = append(findings, found...)
		statuses = append(statuses, ran(step.id))
	}
	return findings, statuses, nil
}

func pgForeignKeys(ctx context.Context, tx *sql.Tx) ([]Finding, error) {
	var findings []Finding
	err := each(ctx, tx, pgForeignKeysQuery, nil, func(rows *sql.Rows) error {
		var row fkRow
		if err := rows.Scan(&row.Schema, &row.Table, &row.Constraint, &row.Partitioned, pq.Array(&row.Columns)); err != nil {
			return err
		}
		findings = append(findings, fkFinding(row))
		return nil
	})
	return findings, err
}

func pgUnusedIndexes(ctx context.Context, tx *sql.Tx) ([]Finding, error) {
	var findings []Finding
	err := each(ctx, tx, pgUnusedIndexesQuery, nil, func(rows *sql.Rows) error {
		var index indexRef
		if err := rows.Scan(&index.Schema, &index.Table, &index.Index); err != nil {
			return err
		}
		findings = append(findings, unusedIndexFinding(EnginePostgres, index))
		return nil
	})
	return findings, err
}

func pgDuplicates(ctx context.Context, tx *sql.Tx) ([]Finding, error) {
	var indexes []indexRow
	err := each(ctx, tx, pgIndexesQuery, nil, func(rows *sql.Rows) error {
		var row indexRow
		if err := rows.Scan(&row.Schema, &row.Table, &row.Index, &row.Key, &row.Primary, &row.Unique, &row.Constraint, &row.Partitioned); err != nil {
			return err
		}
		indexes = append(indexes, row)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return duplicateFindings(EnginePostgres, indexes), nil
}

func pgNoPrimaryKey(ctx context.Context, tx *sql.Tx) ([]Finding, error) {
	var findings []Finding
	err := each(ctx, tx, pgNoPrimaryKeyQuery, nil, func(rows *sql.Rows) error {
		var table tableRef
		if err := rows.Scan(&table.Schema, &table.Table); err != nil {
			return err
		}
		findings = append(findings, noPrimaryKeyFinding(EnginePostgres, table))
		return nil
	})
	return findings, err
}

func pgLargeTables(ctx context.Context, tx *sql.Tx, threshold int64) ([]Finding, error) {
	var findings []Finding
	err := each(ctx, tx, pgLargeTablesQuery, []any{threshold}, func(rows *sql.Rows) error {
		var row largeTableRow
		if err := rows.Scan(&row.Schema, &row.Table, &row.Rows, &row.FullScans, &row.IndexScans); err != nil {
			return err
		}
		findings = append(findings, largeTableFinding(EnginePostgres, row))
		return nil
	})
	return findings, err
}

// each runs a query and hands every row to scan.
func each(ctx context.Context, tx *sql.Tx, query string, args []any, scan func(*sql.Rows) error) error {
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		if err := scan(rows); err != nil {
			return err
		}
	}
	return rows.Err()
}

func ran(id string) CheckStatus {
	return CheckStatus{ID: id, Category: categoryOf[id], Status: StatusRan}
}

var categoryOf = map[string]string{
	CheckFKWithoutIndex:       CategoryPerformance,
	CheckUnusedIndex:          CategoryPerformance,
	CheckDuplicateIndex:       CategoryPerformance,
	CheckNoPrimaryKey:         CategoryPerformance,
	CheckLargeTableNoIndexUse: CategoryPerformance,
	CheckAnonUnfiltered:       CategorySecurity,
	CheckWriteWithoutSelect:   CategorySecurity,
}
