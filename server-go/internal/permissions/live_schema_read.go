package permissions

import (
	"context"
	"database/sql"
	"fmt"
)

// The relation kinds the engine serves: tables, views, materialized views,
// partitioned and foreign tables.
const postgresShapesQuery = `
SELECT c.relname, c.relkind IN ('v', 'm'), a.attname
FROM pg_class c
JOIN pg_namespace n ON n.oid = c.relnamespace
LEFT JOIN pg_attribute a ON a.attrelid = c.oid AND a.attnum > 0 AND NOT a.attisdropped
WHERE n.nspname = $1 AND c.relkind IN ('r', 'v', 'm', 'p', 'f')
ORDER BY c.relname, a.attnum`

// postgresKeysQuery orders rows as schema.Introspector.GetRelationships does,
// which relationship naming relies on.
const postgresKeysQuery = `
SELECT c.conname, sn.nspname, src.relname, sa.attname, tn.nspname, tgt.relname
FROM pg_constraint c
JOIN pg_class src ON src.oid = c.conrelid
JOIN pg_namespace sn ON sn.oid = src.relnamespace
JOIN pg_class tgt ON tgt.oid = c.confrelid
JOIN pg_namespace tn ON tn.oid = tgt.relnamespace
CROSS JOIN LATERAL unnest(c.conkey) WITH ORDINALITY AS k(attnum, position)
JOIN pg_attribute sa ON sa.attrelid = c.conrelid AND sa.attnum = k.attnum
WHERE c.contype = 'f' AND (sn.nspname = $1 OR tn.nspname = $1)
ORDER BY c.conname, k.position`

const mysqlShapesQuery = `
SELECT c.table_name, t.table_type = 'VIEW', c.column_name
FROM information_schema.columns c
JOIN information_schema.tables t ON t.table_schema = c.table_schema AND t.table_name = c.table_name
WHERE c.table_schema = ?
ORDER BY c.table_name, c.ordinal_position`

const mysqlKeysQuery = `
SELECT constraint_name, table_schema, table_name, column_name, referenced_table_schema, referenced_table_name
FROM information_schema.key_column_usage
WHERE referenced_table_name IS NOT NULL AND (table_schema = ? OR referenced_table_schema = ?)
ORDER BY constraint_name, ordinal_position`

// ReadPostgresSchema reads one schema of a Postgres project.
func ReadPostgresSchema(ctx context.Context, db *sql.DB, schemaName string) (*LiveSchema, error) {
	tables, err := readShapes(ctx, db, postgresShapesQuery, schemaName)
	if err != nil {
		return nil, err
	}
	keys, err := readKeys(ctx, db, postgresKeysQuery, schemaName)
	if err != nil {
		return nil, err
	}
	return NewLiveSchema(schemaName, "", tables, keys), nil
}

// ReadMySQLSchema reads a MySQL project's one database; any other schema
// name reads as empty, naming the database.
func ReadMySQLSchema(ctx context.Context, db *sql.DB, schemaName string) (*LiveSchema, error) {
	var database sql.NullString
	if err := db.QueryRowContext(ctx, "SELECT DATABASE()").Scan(&database); err != nil {
		return nil, fmt.Errorf("read database: %w", err)
	}
	if !database.Valid {
		return nil, fmt.Errorf("the connection has no database")
	}
	if schemaName != database.String {
		return NewLiveSchema(schemaName, database.String, nil, nil), nil
	}
	tables, err := readShapes(ctx, db, mysqlShapesQuery, schemaName)
	if err != nil {
		return nil, err
	}
	keys, err := readKeys(ctx, db, mysqlKeysQuery, schemaName, schemaName)
	if err != nil {
		return nil, err
	}
	return NewLiveSchema(schemaName, database.String, tables, keys), nil
}

func readShapes(ctx context.Context, db *sql.DB, query string, args ...any) ([]TableShape, error) {
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("read tables: %w", err)
	}
	defer rows.Close()
	var tables []TableShape
	for rows.Next() {
		var name string
		var view bool
		var column sql.NullString
		if err := rows.Scan(&name, &view, &column); err != nil {
			return nil, fmt.Errorf("scan table: %w", err)
		}
		if len(tables) == 0 || tables[len(tables)-1].Name != name {
			tables = append(tables, TableShape{Name: name, View: view})
		}
		if column.Valid {
			last := &tables[len(tables)-1]
			last.Columns = append(last.Columns, column.String)
		}
	}
	return tables, rows.Err()
}

func readKeys(ctx context.Context, db *sql.DB, query string, args ...any) ([]KeyColumn, error) {
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("read foreign keys: %w", err)
	}
	defer rows.Close()
	var keys []KeyColumn
	for rows.Next() {
		var key KeyColumn
		if err := rows.Scan(&key.Constraint, &key.SourceSchema, &key.SourceTable, &key.SourceColumn,
			&key.TargetSchema, &key.TargetTable); err != nil {
			return nil, fmt.Errorf("scan foreign key: %w", err)
		}
		keys = append(keys, key)
	}
	return keys, rows.Err()
}
