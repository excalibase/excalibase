package schema

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"
)

const sqlWhere = " WHERE "


// allowedOperators is the allowlist of valid filter operators.
var allowedOperators = map[string]string{
	"=":           "=",
	"!=":          "!=",
	">":           ">",
	"<":           "<",
	">=":          ">=",
	"<=":          "<=",
	"like":        "LIKE",
	"ilike":       "ILIKE",
	"is_null":     "IS NULL",
	"is_not_null": "IS NOT NULL",
}

// validateSortOrder validates that order is "asc" or "desc".
func validateSortOrder(order string) error {
	switch strings.ToLower(order) {
	case "asc", "desc":
		return nil
	default:
		return fmt.Errorf("invalid sort order: %q (must be asc or desc)", order)
	}
}

// GetRows returns paginated rows from a table.
func (i *Introspector) GetRows(ctx context.Context, db *sql.DB, schema, table string, opts RowQueryOpts) (*RowsResult, error) {
	opts = applyRowDefaults(opts)

	dataSQL, countSQL, dataArgs, whereArgs, err := buildSelectQuery(schema, table, opts)
	if err != nil {
		return nil, err
	}

	var totalCount int64
	if err := db.QueryRowContext(ctx, countSQL, whereArgs...).Scan(&totalCount); err != nil {
		return nil, fmt.Errorf("count rows: %w", err)
	}

	rows, err := db.QueryContext(ctx, dataSQL, dataArgs...)
	if err != nil {
		return nil, fmt.Errorf("query rows: %w", err)
	}
	defer rows.Close()

	columns, resultRows, err := scanQueryRows(rows)
	if err != nil {
		return nil, err
	}

	return &RowsResult{
		Columns:    columns,
		Rows:       resultRows,
		TotalCount: totalCount,
	}, nil
}

// applyRowDefaults clamps limit and offset to safe ranges.
func applyRowDefaults(opts RowQueryOpts) RowQueryOpts {
	if opts.Limit <= 0 {
		opts.Limit = 50
	}
	if opts.Limit > 1000 {
		opts.Limit = 1000
	}
	if opts.Offset < 0 {
		opts.Offset = 0
	}
	return opts
}

// buildSelectQuery builds the data and count SQL statements from the query options.
func buildSelectQuery(schema, table string, opts RowQueryOpts) (dataSQL, countSQL string, dataArgs, whereArgs []interface{}, err error) {
	fqn := QuoteIdent(schema) + "." + QuoteIdent(table)

	whereClauses, whereArgs, err := buildWhereClause(opts.Filters)
	if err != nil {
		return "", "", nil, nil, fmt.Errorf("build where: %w", err)
	}

	whereSQL := ""
	if len(whereClauses) > 0 {
		whereSQL = sqlWhere + strings.Join(whereClauses, " AND ")
	}

	countSQL = "SELECT COUNT(*) FROM " + fqn + whereSQL
	dataSQL = "SELECT * FROM " + fqn + whereSQL

	if opts.Sort != "" {
		if err := ValidateColumnName(opts.Sort); err != nil {
			return "", "", nil, nil, fmt.Errorf("invalid sort column: %w", err)
		}
		order := "ASC"
		if opts.Order != "" {
			if err := validateSortOrder(opts.Order); err != nil {
				return "", "", nil, nil, err
			}
			order = strings.ToUpper(opts.Order)
		}
		dataSQL += " ORDER BY " + QuoteIdent(opts.Sort) + " " + order
	}

	nextParam := len(whereArgs) + 1
	dataSQL += fmt.Sprintf(" LIMIT $%d OFFSET $%d", nextParam, nextParam+1)
	dataArgs = append(whereArgs, opts.Limit, opts.Offset)

	return dataSQL, countSQL, dataArgs, whereArgs, nil
}

// scanQueryRows extracts column metadata and scans all rows from the result set.
func scanQueryRows(rows *sql.Rows) ([]ColumnMeta, [][]interface{}, error) {
	colTypes, err := rows.ColumnTypes()
	if err != nil {
		return nil, nil, fmt.Errorf("column types: %w", err)
	}

	columns := make([]ColumnMeta, len(colTypes))
	for idx, ct := range colTypes {
		columns[idx] = ColumnMeta{
			Name:     ct.Name(),
			DataType: ct.DatabaseTypeName(),
		}
	}

	resultRows := make([][]interface{}, 0)
	for rows.Next() {
		vals := make([]interface{}, len(colTypes))
		ptrs := make([]interface{}, len(colTypes))
		for idx := range vals {
			ptrs[idx] = &vals[idx]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return nil, nil, fmt.Errorf("scan row: %w", err)
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
		return nil, nil, fmt.Errorf("iterate rows: %w", err)
	}

	return columns, resultRows, nil
}

// InsertRow inserts a new row into a table and returns the inserted row.
func (i *Introspector) InsertRow(ctx context.Context, db *sql.DB, schema, table string, data map[string]interface{}) (*QueryResult, error) {
	if len(data) == 0 {
		return nil, fmt.Errorf("data is required")
	}

	query, args := buildInsertQuery(schema, table, data)

	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("insert row: %w", err)
	}
	defer rows.Close()

	columns, resultRows, err := scanQueryRows(rows)
	if err != nil {
		return nil, err
	}

	return &QueryResult{
		Columns: columns,
		Rows:    resultRows,
	}, nil
}

// buildInsertQuery constructs the INSERT ... RETURNING * SQL and its arguments from a data map.
// Keys are sorted for deterministic parameter ordering.
func buildInsertQuery(schema, table string, data map[string]interface{}) (string, []interface{}) {
	fqn := QuoteIdent(schema) + "." + QuoteIdent(table)

	keys := make([]string, 0, len(data))
	for k := range data {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	quotedCols := make([]string, 0, len(keys))
	placeholders := make([]string, 0, len(keys))
	args := make([]interface{}, 0, len(keys))

	for idx, k := range keys {
		quotedCols = append(quotedCols, QuoteIdent(k))
		placeholders = append(placeholders, fmt.Sprintf("$%d", idx+1))
		args = append(args, data[k])
	}

	query := "INSERT INTO " + fqn +
		" (" + strings.Join(quotedCols, ", ") + ")" +
		" VALUES (" + strings.Join(placeholders, ", ") + ")" +
		" RETURNING *"
	return query, args
}

// UpdateRow updates a row by primary key.
func (i *Introspector) UpdateRow(ctx context.Context, db *sql.DB, schema, table, pkColumn, pkValue string, data map[string]interface{}) error {
	if len(data) == 0 {
		return fmt.Errorf("data is required")
	}

	fqn := QuoteIdent(schema) + "." + QuoteIdent(table)

	// Sort keys for deterministic query building
	keys := make([]string, 0, len(data))
	for k := range data {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	setClauses := make([]string, 0, len(keys))
	args := make([]interface{}, 0, len(keys)+1)

	for idx, k := range keys {
		setClauses = append(setClauses, QuoteIdent(k)+" = "+fmt.Sprintf("$%d", idx+1))
		args = append(args, data[k])
	}

	// PK value is the last parameter
	pkParam := fmt.Sprintf("$%d", len(args)+1)
	args = append(args, pkValue)

	query := "UPDATE " + fqn +
		" SET " + strings.Join(setClauses, ", ") +
		sqlWhere + QuoteIdent(pkColumn) + " = " + pkParam

	if _, err := db.ExecContext(ctx, query, args...); err != nil {
		return fmt.Errorf("update row: %w", err)
	}
	return nil
}

// DeleteRow deletes a row by primary key.
func (i *Introspector) DeleteRow(ctx context.Context, db *sql.DB, schema, table, pkColumn, pkValue string) error {
	fqn := QuoteIdent(schema) + "." + QuoteIdent(table)
	query := "DELETE FROM " + fqn + sqlWhere + QuoteIdent(pkColumn) + " = $1"
	if _, err := db.ExecContext(ctx, query, pkValue); err != nil {
		return fmt.Errorf("delete row: %w", err)
	}
	return nil
}

// buildWhereClause builds WHERE clause fragments and corresponding args from filters.
func buildWhereClause(filters []RowFilter) ([]string, []interface{}, error) {
	if len(filters) == 0 {
		return nil, nil, nil
	}

	clauses := make([]string, 0, len(filters))
	args := make([]interface{}, 0, len(filters))
	paramIdx := 1

	for _, f := range filters {
		if err := ValidateColumnName(f.Column); err != nil {
			return nil, nil, fmt.Errorf("invalid filter column: %w", err)
		}
		sqlOp, ok := allowedOperators[strings.ToLower(f.Operator)]
		if !ok {
			return nil, nil, fmt.Errorf("invalid operator: %q", f.Operator)
		}

		col := QuoteIdent(f.Column)

		switch strings.ToLower(f.Operator) {
		case "is_null":
			clauses = append(clauses, col+" IS NULL")
		case "is_not_null":
			clauses = append(clauses, col+" IS NOT NULL")
		default:
			clauses = append(clauses, col+" "+sqlOp+" "+fmt.Sprintf("$%d", paramIdx))
			args = append(args, f.Value)
			paramIdx++
		}
	}

	return clauses, args, nil
}
