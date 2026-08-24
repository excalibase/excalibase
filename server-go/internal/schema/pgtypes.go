package schema

import (
	"context"
	"database/sql"
	"fmt"
)

// GetTypes returns user-defined types (enum, composite, domain, range) in the given schema.
func (i *Introspector) GetTypes(ctx context.Context, db *sql.DB, schemaName string) ([]PgTypeInfo, error) {
	rows, err := db.QueryContext(ctx, pgTypesQuery, schemaName)
	if err != nil {
		return nil, fmt.Errorf("query types: %w", err)
	}
	defer rows.Close()

	types := make([]PgTypeInfo, 0)
	for rows.Next() {
		var pt PgTypeInfo
		var typType string
		if err := rows.Scan(&pt.Name, &pt.Schema, &typType); err != nil {
			return nil, fmt.Errorf("scan type: %w", err)
		}
		switch typType {
		case "e":
			pt.Type = "enum"
		case "c":
			pt.Type = "composite"
		case "d":
			pt.Type = "domain"
		case "r":
			pt.Type = "range"
		default:
			pt.Type = typType
		}
		pt.Values = make([]string, 0)
		types = append(types, pt)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Fetch enum values for enum types
	for idx := range types {
		if types[idx].Type == "enum" {
			values, err := i.getEnumValues(ctx, db, types[idx].Schema, types[idx].Name)
			if err != nil {
				return nil, fmt.Errorf("get enum values for %s: %w", types[idx].Name, err)
			}
			types[idx].Values = values
		}
	}

	return types, nil
}

func (i *Introspector) getEnumValues(ctx context.Context, db *sql.DB, schema, typeName string) ([]string, error) {
	rows, err := db.QueryContext(ctx, enumValuesQuery, schema, typeName)
	if err != nil {
		return nil, fmt.Errorf("query enum values: %w", err)
	}
	defer rows.Close()

	values := make([]string, 0)
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			return nil, fmt.Errorf("scan enum value: %w", err)
		}
		values = append(values, v)
	}
	return values, rows.Err()
}

const pgTypesQuery = `
SELECT
    t.typname AS name,
    n.nspname AS schema,
    t.typtype
FROM pg_type t
JOIN pg_namespace n ON n.oid = t.typnamespace
WHERE n.nspname = $1
AND t.typtype IN ('e', 'c', 'd', 'r')
AND t.typname NOT LIKE '\_%'
ORDER BY t.typname`

const enumValuesQuery = `
SELECT e.enumlabel
FROM pg_enum e
JOIN pg_type t ON t.oid = e.enumtypid
JOIN pg_namespace n ON n.oid = t.typnamespace
WHERE n.nspname = $1 AND t.typname = $2
ORDER BY e.enumsortorder`
