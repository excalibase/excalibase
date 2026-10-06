package schema

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// The SQL runner answers Studio, not an export: past these the result is cut
// short and flagged, so one tenant's wide query cannot fill the control
// plane's memory.
const (
	MaxQueryRows  = 1000
	MaxQueryBytes = 5 << 20

	defaultStatementTimeout = 30 * time.Second
	// cancelGrace lets the server-side timeout answer first with its own
	// message; the client deadline is the backstop the tenant cannot lift.
	cancelGrace = 2 * time.Second
)

// Introspector provides PostgreSQL schema introspection.
type Introspector struct {
	statementTimeout time.Duration
}

func NewIntrospector() *Introspector {
	return &Introspector{statementTimeout: defaultStatementTimeout}
}

// WithStatementTimeout bounds every statement the SQL runner executes.
func (i *Introspector) WithStatementTimeout(d time.Duration) *Introspector {
	i.statementTimeout = d
	return i
}

// bounded gives a runner call its deadline. A statement past it is cancelled
// on the server even if the tenant's SQL turned statement_timeout off.
func (i *Introspector) bounded(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, i.statementTimeout+cancelGrace)
}

func (i *Introspector) GetTables(ctx context.Context, db *sql.DB, schemaName string) ([]TableInfo, error) {
	rows, err := db.QueryContext(ctx, tablesQuery, schemaName)
	if err != nil {
		return nil, fmt.Errorf("query tables: %w", err)
	}
	defer rows.Close()

	tables := make([]TableInfo, 0)
	for rows.Next() {
		var t TableInfo
		if err := rows.Scan(&t.Name, &t.Schema, &t.Type); err != nil {
			return nil, fmt.Errorf("scan table: %w", err)
		}
		tables = append(tables, t)
	}
	return tables, rows.Err()
}

func (i *Introspector) GetColumns(ctx context.Context, db *sql.DB, schemaName, tableName string) ([]ColumnInfo, error) {
	rows, err := db.QueryContext(ctx, columnsQuery, schemaName, tableName)
	if err != nil {
		return nil, fmt.Errorf("query columns: %w", err)
	}
	defer rows.Close()

	columns := make([]ColumnInfo, 0)
	for rows.Next() {
		var c ColumnInfo
		var isNullable string
		if err := rows.Scan(
			&c.Name, &c.DataType, &isNullable, &c.DefaultValue,
			&c.OrdinalPosition, &c.CharacterMaximumLength,
			&c.NumericPrecision, &c.NumericScale,
			&c.PrimaryKey, &c.Unique,
		); err != nil {
			return nil, fmt.Errorf("scan column: %w", err)
		}
		c.Nullable = isNullable == "YES"
		columns = append(columns, c)
	}
	return columns, rows.Err()
}

func (i *Introspector) GetRelationships(ctx context.Context, db *sql.DB, schemaName string) ([]RelationshipInfo, error) {
	rows, err := db.QueryContext(ctx, relationshipsQuery, schemaName)
	if err != nil {
		return nil, fmt.Errorf("query relationships: %w", err)
	}
	defer rows.Close()

	rels := make([]RelationshipInfo, 0)
	for rows.Next() {
		var r RelationshipInfo
		if err := rows.Scan(&r.ConstraintName, &r.SourceTable, &r.SourceColumn,
			&r.TargetTable, &r.TargetColumn, &r.OnDelete, &r.OnUpdate); err != nil {
			return nil, fmt.Errorf("scan relationship: %w", err)
		}
		rels = append(rels, r)
	}
	return rels, rows.Err()
}

func (i *Introspector) GetIndexes(ctx context.Context, db *sql.DB, schemaName, tableName string) ([]IndexInfo, error) {
	rows, err := db.QueryContext(ctx, indexesQuery, schemaName, tableName)
	if err != nil {
		return nil, fmt.Errorf("query indexes: %w", err)
	}
	defer rows.Close()

	indexes := make([]IndexInfo, 0)
	for rows.Next() {
		var idx IndexInfo
		var indexDef string
		if err := rows.Scan(&idx.Name, &idx.TableName, &indexDef); err != nil {
			return nil, fmt.Errorf("scan index: %w", err)
		}
		idx.Unique = strings.Contains(strings.ToUpper(indexDef), "UNIQUE")
		idx.Type = parseIndexType(indexDef)
		idx.Columns = parseIndexColumns(indexDef)
		indexes = append(indexes, idx)
	}
	return indexes, rows.Err()
}

func (i *Introspector) ExecuteDDL(ctx context.Context, db *sql.DB, ddl string) DDLResult {
	ctx, cancel := i.bounded(ctx)
	defer cancel()
	result, err := db.ExecContext(ctx, ddl)
	if err != nil {
		return DDLResult{Success: false, Error: userMessage(err)}
	}
	rowsAffected, _ := result.RowsAffected()
	return DDLResult{Success: true, UpdateCount: int(rowsAffected)}
}

// isReadQuery returns true if the query starts with a read-only SQL keyword.
func isReadQuery(query string) bool {
	trimmed := strings.TrimSpace(strings.ToUpper(query))
	return strings.HasPrefix(trimmed, "SELECT") ||
		strings.HasPrefix(trimmed, "EXPLAIN") ||
		strings.HasPrefix(trimmed, "WITH") ||
		strings.HasPrefix(trimmed, "SHOW") ||
		strings.HasPrefix(trimmed, "TABLE") ||
		strings.HasPrefix(trimmed, "VALUES")
}

func (i *Introspector) ExecuteQuery(ctx context.Context, db *sql.DB, query string) QueryResult {
	ctx, cancel := i.bounded(ctx)
	defer cancel()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return QueryResult{Error: userMessage(fmt.Errorf("begin tx: %w", err))}
	}
	defer tx.Rollback()

	timeout := fmt.Sprintf("SET LOCAL statement_timeout = %d", i.statementTimeout.Milliseconds())
	if _, err := tx.ExecContext(ctx, timeout); err != nil {
		return QueryResult{Error: userMessage(fmt.Errorf("set timeout: %w", err))}
	}

	if isReadQuery(query) {
		return i.executeReadQuery(ctx, tx, query)
	}
	return i.executeDMLQuery(ctx, tx, query)
}

// executeReadQuery runs a SELECT/EXPLAIN/WITH/SHOW query inside tx and returns the row set.
func (i *Introspector) executeReadQuery(ctx context.Context, tx interface {
	QueryContext(context.Context, string, ...interface{}) (*sql.Rows, error)
	Commit() error
}, query string) QueryResult {
	rows, err := tx.QueryContext(ctx, query)
	if err != nil {
		return QueryResult{Error: userMessage(err)}
	}
	result := readResult(rows)
	if result.Error != "" {
		return result
	}
	if err := tx.Commit(); err != nil {
		return QueryResult{Error: userMessage(fmt.Errorf("commit: %w", err))}
	}
	return result
}

// readResult reads a row set up to the runner's caps and closes it.
func readResult(rows *sql.Rows) QueryResult {
	defer rows.Close()
	colTypes, err := rows.ColumnTypes()
	if err != nil {
		return QueryResult{Error: userMessage(fmt.Errorf("column types: %w", err))}
	}
	columns := make([]ColumnMeta, len(colTypes))
	for idx, ct := range colTypes {
		columns[idx] = ColumnMeta{Name: ct.Name(), DataType: ct.DatabaseTypeName()}
	}
	resultRows, truncated, err := collectRows(rows, len(colTypes))
	if err != nil {
		return QueryResult{Error: userMessage(err)}
	}
	// Closing drains what was not kept, so a data-modifying WITH still commits.
	if err := rows.Close(); err != nil {
		return QueryResult{Error: userMessage(err)}
	}
	return QueryResult{Columns: columns, Rows: resultRows, Truncated: truncated}
}

// collectRows keeps rows until MaxQueryRows or MaxQueryBytes would be passed.
func collectRows(rows *sql.Rows, width int) ([][]interface{}, bool, error) {
	var kept [][]interface{}
	held := 0
	for rows.Next() {
		if len(kept) == MaxQueryRows {
			return kept, true, nil
		}
		row, size, err := scanRow(rows, width)
		if err != nil {
			return nil, false, err
		}
		if held+size > MaxQueryBytes {
			return kept, true, nil
		}
		held += size
		kept = append(kept, row)
	}
	return kept, false, rows.Err()
}

func scanRow(rows *sql.Rows, width int) ([]interface{}, int, error) {
	vals := make([]interface{}, width)
	ptrs := make([]interface{}, width)
	for idx := range vals {
		ptrs[idx] = &vals[idx]
	}
	if err := rows.Scan(ptrs...); err != nil {
		return nil, 0, fmt.Errorf("scan row: %w", err)
	}
	size := 0
	for idx, v := range vals {
		if b, ok := v.([]byte); ok {
			vals[idx] = string(b)
			size += len(b)
		} else if str, ok := v.(string); ok {
			size += len(str)
		} else {
			size += 16
		}
	}
	return vals, size, nil
}

// executeDMLQuery runs a DML/DDL statement inside tx and returns the affected-row count.
func (i *Introspector) executeDMLQuery(ctx context.Context, tx interface {
	ExecContext(context.Context, string, ...interface{}) (sql.Result, error)
	Commit() error
}, query string) QueryResult {
	result, err := tx.ExecContext(ctx, query)
	if err != nil {
		return QueryResult{Error: userMessage(err)}
	}
	affected, _ := result.RowsAffected()
	if err := tx.Commit(); err != nil {
		return QueryResult{Error: userMessage(fmt.Errorf("commit: %w", err))}
	}
	return QueryResult{Command: "EXEC", AffectedRows: affected}
}

func (i *Introspector) TestConnection(ctx context.Context, db *sql.DB) bool {
	return db.PingContext(ctx) == nil
}

// --- SQL queries ---

const tablesQuery = `
SELECT table_name, table_schema, table_type
FROM information_schema.tables
WHERE table_schema = $1
AND table_type IN ('BASE TABLE', 'VIEW')
ORDER BY table_name`

const columnsQuery = `
SELECT c.column_name, c.data_type, c.is_nullable, c.column_default,
       c.ordinal_position, c.character_maximum_length,
       c.numeric_precision, c.numeric_scale,
       COALESCE(
           (SELECT true FROM pg_constraint con
            JOIN pg_attribute a ON a.attrelid = con.conrelid AND a.attnum = ANY(con.conkey)
            WHERE con.contype = 'p'
            AND con.conrelid = (SELECT oid FROM pg_class WHERE relname = c.table_name AND relnamespace = (SELECT oid FROM pg_namespace WHERE nspname = c.table_schema))
            AND a.attname = c.column_name), false
       ) AS is_primary_key,
       COALESCE(
           (SELECT true FROM pg_constraint con
            JOIN pg_attribute a ON a.attrelid = con.conrelid AND a.attnum = ANY(con.conkey)
            WHERE con.contype = 'u'
            AND con.conrelid = (SELECT oid FROM pg_class WHERE relname = c.table_name AND relnamespace = (SELECT oid FROM pg_namespace WHERE nspname = c.table_schema))
            AND a.attname = c.column_name), false
       ) AS is_unique
FROM information_schema.columns c
WHERE c.table_schema = $1 AND c.table_name = $2
ORDER BY c.ordinal_position`

const relationshipsQuery = `
SELECT
    c.conname AS constraint_name,
    src.relname AS source_table,
    sa.attname AS source_column,
    tgt.relname AS target_table,
    ta.attname AS target_column,
    CASE c.confdeltype
        WHEN 'a' THEN 'NO ACTION' WHEN 'r' THEN 'RESTRICT'
        WHEN 'c' THEN 'CASCADE' WHEN 'n' THEN 'SET NULL'
        WHEN 'd' THEN 'SET DEFAULT' ELSE 'NO ACTION'
    END AS on_delete,
    CASE c.confupdtype
        WHEN 'a' THEN 'NO ACTION' WHEN 'r' THEN 'RESTRICT'
        WHEN 'c' THEN 'CASCADE' WHEN 'n' THEN 'SET NULL'
        WHEN 'd' THEN 'SET DEFAULT' ELSE 'NO ACTION'
    END AS on_update
FROM pg_constraint c
JOIN pg_class src ON src.oid = c.conrelid
JOIN pg_class tgt ON tgt.oid = c.confrelid
JOIN pg_namespace n ON n.oid = src.relnamespace
JOIN pg_attribute sa ON sa.attrelid = c.conrelid AND sa.attnum = ANY(c.conkey)
JOIN pg_attribute ta ON ta.attrelid = c.confrelid AND ta.attnum = ANY(c.confkey)
WHERE c.contype = 'f' AND n.nspname = $1
ORDER BY c.conname`

const indexesQuery = `
SELECT indexname, tablename, indexdef
FROM pg_indexes
WHERE schemaname = $1 AND tablename = $2
ORDER BY indexname`

// --- helpers ---

func parseIndexType(def string) string {
	upper := strings.ToUpper(def)
	switch {
	case strings.Contains(upper, "USING HASH"):
		return "hash"
	case strings.Contains(upper, "USING GIN"):
		return "gin"
	case strings.Contains(upper, "USING GIST"):
		return "gist"
	case strings.Contains(upper, "USING BRIN"):
		return "brin"
	default:
		return "btree"
	}
}

func parseIndexColumns(def string) []string {
	start := strings.LastIndex(def, "(")
	end := strings.LastIndex(def, ")")
	if start < 0 || end < 0 || end <= start {
		return []string{}
	}
	colsPart := def[start+1 : end]
	parts := strings.Split(colsPart, ",")
	cols := make([]string, 0, len(parts))
	for _, p := range parts {
		cols = append(cols, strings.TrimSpace(p))
	}
	return cols
}
