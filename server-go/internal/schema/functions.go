package schema

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// GetFunctions returns user-defined functions in the given schema.
func (i *Introspector) GetFunctions(ctx context.Context, db *sql.DB, schema string) ([]FunctionInfo, error) {
	rows, err := db.QueryContext(ctx, functionsQuery, schema)
	if err != nil {
		return nil, fmt.Errorf("query functions: %w", err)
	}
	defer rows.Close()

	funcs := make([]FunctionInfo, 0)
	for rows.Next() {
		var f FunctionInfo
		var volatility string
		if err := rows.Scan(&f.Name, &f.Schema, &f.Language, &f.ReturnType, &f.ArgTypes, &volatility, &f.Definition); err != nil {
			return nil, fmt.Errorf("scan function: %w", err)
		}
		switch volatility {
		case "v":
			f.Volatility = "VOLATILE"
		case "s":
			f.Volatility = "STABLE"
		case "i":
			f.Volatility = "IMMUTABLE"
		default:
			f.Volatility = volatility
		}
		funcs = append(funcs, f)
	}
	return funcs, rows.Err()
}

// CreateFunction creates a new PostgreSQL function.
func (i *Introspector) CreateFunction(ctx context.Context, db *sql.DB, req CreateFunctionRequest) error {
	schemaName := req.Schema
	if schemaName == "" {
		schemaName = "public"
	}

	if err := ValidateLanguage(req.Language); err != nil {
		return err
	}
	if err := ValidateVolatility(req.Volatility); err != nil {
		return err
	}
	if err := ValidateTypeName(req.ReturnType); err != nil {
		return fmt.Errorf("invalid return type: %w", err)
	}
	if err := ValidateArgTypes(req.Args); err != nil {
		return err
	}

	var b strings.Builder
	b.WriteString("CREATE OR REPLACE FUNCTION ")
	b.WriteString(QuoteIdent(schemaName))
	b.WriteString(".")
	b.WriteString(QuoteIdent(req.Name))
	b.WriteString("(")
	b.WriteString(req.Args)
	b.WriteString(") RETURNS ")
	b.WriteString(req.ReturnType)
	b.WriteString(" LANGUAGE ")
	b.WriteString(strings.ToLower(req.Language))
	b.WriteString(" ")
	b.WriteString(strings.ToUpper(req.Volatility))
	b.WriteString(" AS ")
	b.WriteString(QuoteLiteral(req.Body))

	if _, err := db.ExecContext(ctx, b.String()); err != nil {
		return fmt.Errorf("create function: %w", err)
	}
	return nil
}

// DropFunction drops a function by name and argument types.
func (i *Introspector) DropFunction(ctx context.Context, db *sql.DB, schema, name, argTypes string) error {
	if err := ValidateArgTypes(argTypes); err != nil {
		return err
	}
	stmt := "DROP FUNCTION " + QuoteIdent(schema) + "." + QuoteIdent(name) +
		"(" + argTypes + ")"
	if _, err := db.ExecContext(ctx, stmt); err != nil {
		return fmt.Errorf("drop function: %w", err)
	}
	return nil
}

const functionsQuery = `
SELECT
    p.proname AS name,
    n.nspname AS schema,
    l.lanname AS language,
    pg_get_function_result(p.oid) AS return_type,
    pg_get_function_identity_arguments(p.oid) AS arg_types,
    p.provolatile AS volatility,
    pg_get_functiondef(p.oid) AS definition
FROM pg_proc p
JOIN pg_namespace n ON n.oid = p.pronamespace
JOIN pg_language l ON l.oid = p.prolang
WHERE n.nspname = $1
AND p.prokind = 'f'
AND l.lanname != 'internal'
AND l.lanname != 'c'
ORDER BY p.proname`
