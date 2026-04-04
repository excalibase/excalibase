package schema

import (
	"context"
	"database/sql"
	"fmt"
)

// GetExtensions returns installed and available extensions.
func (i *Introspector) GetExtensions(ctx context.Context, db *sql.DB) ([]ExtensionInfo, error) {
	rows, err := db.QueryContext(ctx, extensionsQuery)
	if err != nil {
		return nil, fmt.Errorf("query extensions: %w", err)
	}
	defer rows.Close()

	exts := make([]ExtensionInfo, 0)
	for rows.Next() {
		var e ExtensionInfo
		if err := rows.Scan(&e.Name, &e.InstalledVersion, &e.DefaultVersion, &e.Schema, &e.Comment); err != nil {
			return nil, fmt.Errorf("scan extension: %w", err)
		}
		exts = append(exts, e)
	}
	return exts, rows.Err()
}

// CreateExtension installs a PostgreSQL extension.
func (i *Introspector) CreateExtension(ctx context.Context, db *sql.DB, name, extSchema string) error {
	stmt := "CREATE EXTENSION IF NOT EXISTS " + QuoteIdent(name)
	if extSchema != "" {
		stmt += " SCHEMA " + QuoteIdent(extSchema)
	}
	if _, err := db.ExecContext(ctx, stmt); err != nil {
		return fmt.Errorf("create extension: %w", err)
	}
	return nil
}

// DropExtension removes a PostgreSQL extension.
func (i *Introspector) DropExtension(ctx context.Context, db *sql.DB, name string, cascade bool) error {
	stmt := "DROP EXTENSION " + QuoteIdent(name)
	if cascade {
		stmt += " CASCADE"
	}
	if _, err := db.ExecContext(ctx, stmt); err != nil {
		return fmt.Errorf("drop extension: %w", err)
	}
	return nil
}

const extensionsQuery = `
SELECT
    a.name,
    e.extversion AS installed_version,
    a.default_version,
    n.nspname AS schema,
    a.comment
FROM pg_available_extensions a
LEFT JOIN pg_extension e ON e.extname = a.name
LEFT JOIN pg_namespace n ON n.oid = e.extnamespace
ORDER BY a.name`
