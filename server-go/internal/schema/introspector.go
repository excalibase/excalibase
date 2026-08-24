package schema

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// Introspector provides PostgreSQL schema introspection.
type Introspector struct{}

func NewIntrospector() *Introspector {
	return &Introspector{}
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
	result, err := db.ExecContext(ctx, ddl)
	if err != nil {
		return DDLResult{Success: false, Error: err.Error()}
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
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return QueryResult{Error: fmt.Errorf("begin tx: %w", err).Error()}
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, "SET LOCAL statement_timeout = '30s'"); err != nil {
		return QueryResult{Error: fmt.Errorf("set timeout: %w", err).Error()}
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
		return QueryResult{Error: err.Error()}
	}
	defer rows.Close()

	colTypes, err := rows.ColumnTypes()
	if err != nil {
		return QueryResult{Error: fmt.Errorf("column types: %w", err).Error()}
	}

	columns := make([]ColumnMeta, len(colTypes))
	for idx, ct := range colTypes {
		columns[idx] = ColumnMeta{Name: ct.Name(), DataType: ct.DatabaseTypeName()}
	}

	var resultRows [][]interface{}
	for rows.Next() {
		vals := make([]interface{}, len(colTypes))
		ptrs := make([]interface{}, len(colTypes))
		for idx := range vals {
			ptrs[idx] = &vals[idx]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return QueryResult{Error: fmt.Errorf("scan row: %w", err).Error()}
		}
		row := make([]interface{}, len(vals))
		for idx, v := range vals {
			if b, ok := v.([]byte); ok {
				row[idx] = string(b)
			} else {
				row[idx] = v
			}
		}
		resultRows = append(resultRows, row)
	}
	if err := rows.Err(); err != nil {
		return QueryResult{Error: err.Error()}
	}
	tx.Commit()
	return QueryResult{Columns: columns, Rows: resultRows}
}

// executeDMLQuery runs a DML/DDL statement inside tx and returns the affected-row count.
func (i *Introspector) executeDMLQuery(ctx context.Context, tx interface {
	ExecContext(context.Context, string, ...interface{}) (sql.Result, error)
	Commit() error
}, query string) QueryResult {
	result, err := tx.ExecContext(ctx, query)
	if err != nil {
		return QueryResult{Error: err.Error()}
	}
	affected, _ := result.RowsAffected()
	if err := tx.Commit(); err != nil {
		return QueryResult{Error: fmt.Errorf("commit: %w", err).Error()}
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
