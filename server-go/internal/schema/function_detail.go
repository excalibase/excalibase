package schema

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
)

// FunctionDetail is what deciding whether a function can be tracked needs:
// its kind, volatility, owner rights, the table whose rows it returns and
// its input arguments.
type FunctionDetail struct {
	Schema string
	Name   string
	// Kind is pg_proc.prokind: f function, p procedure, a aggregate, w window.
	Kind            string
	Volatility      string // VOLATILE, STABLE or IMMUTABLE
	SecurityDefiner bool
	ReturnsSet      bool
	// ReturnsTable is "schema.relation" when the function returns rows of a
	// table, view, materialized view, partitioned or foreign table, else "".
	ReturnsTable string
	Args         []FunctionArg
}

// FunctionArg is one input argument (IN, INOUT or VARIADIC).
type FunctionArg struct {
	Name string `json:"name"`
	Type string `json:"type"`
}

// FunctionDetails returns every routine named name, in schemaName or, when
// schemaName is "", in any schema but the system ones. Several rows mean the
// name is overloaded.
func (i *Introspector) FunctionDetails(ctx context.Context, db *sql.DB, schemaName, name string) ([]FunctionDetail, error) {
	ctx, cancel := i.bounded(ctx)
	defer cancel()
	rows, err := db.QueryContext(ctx, functionDetailQuery, schemaName, name)
	if err != nil {
		return nil, fmt.Errorf("query function details: %w", err)
	}
	defer rows.Close()

	out := make([]FunctionDetail, 0)
	for rows.Next() {
		var (
			detail     FunctionDetail
			volatility string
			args       string
		)
		if err := rows.Scan(&detail.Schema, &detail.Name, &detail.Kind, &volatility, &detail.SecurityDefiner,
			&detail.ReturnsSet, &detail.ReturnsTable, &args); err != nil {
			return nil, fmt.Errorf("scan function details: %w", err)
		}
		detail.Volatility = volatilityName(volatility)
		if err := json.Unmarshal([]byte(args), &detail.Args); err != nil {
			return nil, fmt.Errorf("decode function arguments: %w", err)
		}
		out = append(out, detail)
	}
	return out, rows.Err()
}

func volatilityName(code string) string {
	switch code {
	case "v":
		return "VOLATILE"
	case "s":
		return "STABLE"
	case "i":
		return "IMMUTABLE"
	}
	return code
}

// TableColumns returns every table-like relation outside the system schemas,
// keyed "schema.relation", with its columns in ordinal order.
func (i *Introspector) TableColumns(ctx context.Context, db *sql.DB) (map[string][]string, error) {
	ctx, cancel := i.bounded(ctx)
	defer cancel()
	rows, err := db.QueryContext(ctx, tableColumnsQuery)
	if err != nil {
		return nil, fmt.Errorf("query table columns: %w", err)
	}
	defer rows.Close()

	out := map[string][]string{}
	for rows.Next() {
		var schemaName, table, column string
		if err := rows.Scan(&schemaName, &table, &column); err != nil {
			return nil, fmt.Errorf("scan table columns: %w", err)
		}
		key := schemaName + "." + table
		out[key] = append(out[key], column)
	}
	return out, rows.Err()
}

const systemSchemaFilter = `n.nspname NOT IN ('pg_catalog', 'information_schema') AND n.nspname NOT LIKE 'pg\_%'`

const functionDetailQuery = `
SELECT
    n.nspname,
    p.proname,
    p.prokind::text,
    p.provolatile::text,
    p.prosecdef,
    p.proretset,
    COALESCE((
        SELECT rn.nspname || '.' || rc.relname
        FROM pg_type rt
        JOIN pg_class rc ON rc.oid = rt.typrelid
        JOIN pg_namespace rn ON rn.oid = rc.relnamespace
        WHERE rt.oid = p.prorettype AND rc.relkind IN ('r', 'v', 'm', 'p', 'f')
    ), ''),
    COALESCE((
        SELECT json_agg(json_build_object(
                   'name', COALESCE(p.proargnames[arg.ord], ''),
                   'type', format_type(arg.typ, NULL)) ORDER BY arg.ord)
        FROM unnest(COALESCE(p.proallargtypes, p.proargtypes::oid[])) WITH ORDINALITY AS arg(typ, ord)
        WHERE COALESCE(p.proargmodes[arg.ord]::text, 'i') IN ('i', 'b', 'v')
    ), '[]')::text
FROM pg_proc p
JOIN pg_namespace n ON n.oid = p.pronamespace
WHERE p.proname = $2
  AND ($1 = '' OR n.nspname = $1)
  AND ` + systemSchemaFilter + `
ORDER BY n.nspname, p.oid`

const tableColumnsQuery = `
SELECT n.nspname, c.relname, a.attname
FROM pg_class c
JOIN pg_namespace n ON n.oid = c.relnamespace
JOIN pg_attribute a ON a.attrelid = c.oid AND a.attnum > 0 AND NOT a.attisdropped
WHERE c.relkind IN ('r', 'v', 'm', 'p', 'f')
  AND ` + systemSchemaFilter + `
ORDER BY n.nspname, c.relname, a.attnum`
