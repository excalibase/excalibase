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
           (SELECT true FROM information_schema.table_constraints tc
            JOIN information_schema.key_column_usage kcu
            ON tc.constraint_name = kcu.constraint_name
            AND tc.table_schema = kcu.table_schema
            WHERE tc.constraint_type = 'PRIMARY KEY'
            AND tc.table_schema = c.table_schema
            AND tc.table_name = c.table_name
            AND kcu.column_name = c.column_name), false
       ) AS is_primary_key,
       COALESCE(
           (SELECT true FROM information_schema.table_constraints tc
            JOIN information_schema.key_column_usage kcu
            ON tc.constraint_name = kcu.constraint_name
            AND tc.table_schema = kcu.table_schema
            WHERE tc.constraint_type = 'UNIQUE'
            AND tc.table_schema = c.table_schema
            AND tc.table_name = c.table_name
            AND kcu.column_name = c.column_name), false
       ) AS is_unique
FROM information_schema.columns c
WHERE c.table_schema = $1 AND c.table_name = $2
ORDER BY c.ordinal_position`

const relationshipsQuery = `
SELECT
    tc.constraint_name,
    kcu.table_name AS source_table,
    kcu.column_name AS source_column,
    ccu.table_name AS target_table,
    ccu.column_name AS target_column,
    rc.delete_rule AS on_delete,
    rc.update_rule AS on_update
FROM information_schema.table_constraints tc
JOIN information_schema.key_column_usage kcu
    ON tc.constraint_name = kcu.constraint_name
    AND tc.table_schema = kcu.table_schema
JOIN information_schema.constraint_column_usage ccu
    ON ccu.constraint_name = tc.constraint_name
    AND ccu.table_schema = tc.table_schema
JOIN information_schema.referential_constraints rc
    ON rc.constraint_name = tc.constraint_name
    AND rc.constraint_schema = tc.table_schema
WHERE tc.constraint_type = 'FOREIGN KEY'
AND tc.table_schema = $1
ORDER BY tc.constraint_name`

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
