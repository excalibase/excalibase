package schema

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// GetRows returns a page of a table's rows.
func (m *MySQLCatalog) GetRows(ctx context.Context, db *sql.DB, schemaName, table string, opts RowQueryOpts) (*RowsResult, error) {
	if _, err := m.database(ctx, db, schemaName); err != nil {
		return nil, err
	}
	dataSQL, countSQL, dataArgs, countArgs, err := buildMySQLSelect(table, applyRowDefaults(opts))
	if err != nil {
		return nil, err
	}
	var total int64
	if err := db.QueryRowContext(ctx, countSQL, countArgs...).Scan(&total); err != nil {
		return nil, fmt.Errorf("count rows: %w", err)
	}
	rows, err := db.QueryContext(ctx, dataSQL, dataArgs...)
	if err != nil {
		return nil, fmt.Errorf("query rows: %w", err)
	}
	defer rows.Close()
	columns, values, err := scanQueryRows(rows)
	if err != nil {
		return nil, err
	}
	return &RowsResult{Columns: columns, Rows: values, TotalCount: total}, nil
}

func buildMySQLSelect(table string, opts RowQueryOpts) (dataSQL, countSQL string, dataArgs, countArgs []interface{}, err error) {
	where, countArgs, err := mysqlWhere(opts.Filters)
	if err != nil {
		return "", "", nil, nil, err
	}
	from := " FROM " + QuoteMySQLIdent(table) + where
	countSQL = "SELECT COUNT(*)" + from
	dataSQL = "SELECT *" + from
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
		dataSQL += " ORDER BY " + QuoteMySQLIdent(opts.Sort) + " " + order
	}
	dataSQL += " LIMIT ? OFFSET ?"
	dataArgs = append(append([]interface{}{}, countArgs...), opts.Limit, opts.Offset)
	return dataSQL, countSQL, dataArgs, countArgs, nil
}

// mysqlWhere is the filters as MySQL takes them; ilike compares lower case,
// whatever the column's collation.
func mysqlWhere(filters []RowFilter) (string, []interface{}, error) {
	clauses := make([]string, 0, len(filters))
	var args []interface{}
	for _, filter := range filters {
		if err := ValidateColumnName(filter.Column); err != nil {
			return "", nil, fmt.Errorf("invalid filter column: %w", err)
		}
		operator := strings.ToLower(filter.Operator)
		sqlOperator, ok := allowedOperators[operator]
		if !ok {
			return "", nil, invalidInput("invalid operator: %q", filter.Operator)
		}
		column := QuoteMySQLIdent(filter.Column)
		switch operator {
		case "is_null", "is_not_null":
			clauses = append(clauses, column+" "+sqlOperator)
			continue
		case "ilike":
			clauses = append(clauses, "LOWER("+column+") LIKE LOWER(?)")
		default:
			clauses = append(clauses, column+" "+sqlOperator+" ?")
		}
		args = append(args, filter.Value)
	}
	if len(clauses) == 0 {
		return "", nil, nil
	}
	return sqlWhere + strings.Join(clauses, " AND "), args, nil
}

func buildMySQLInsert(table string, data map[string]interface{}) (string, []interface{}, error) {
	keys := sortedKeys(data)
	columns := make([]string, 0, len(keys))
	placeholders := make([]string, 0, len(keys))
	args := make([]interface{}, 0, len(keys))
	for _, key := range keys {
		value, err := rowArg(data[key])
		if err != nil {
			return "", nil, fmt.Errorf("column %q: %w", key, err)
		}
		columns = append(columns, QuoteMySQLIdent(key))
		placeholders = append(placeholders, "?")
		args = append(args, value)
	}
	return "INSERT INTO " + QuoteMySQLIdent(table) + " (" + strings.Join(columns, ", ") + ") VALUES (" +
		strings.Join(placeholders, ", ") + ")", args, nil
}

// InsertRow inserts a row and reads it back by its primary key: the values
// sent, or the AUTO_INCREMENT id MySQL generated. A row whose key cannot be
// known (no key, or one filled by an expression) is reported as inserted
// without being read back.
func (m *MySQLCatalog) InsertRow(ctx context.Context, db *sql.DB, schemaName, table string, data map[string]interface{}) (*QueryResult, error) {
	database, err := m.database(ctx, db, schemaName)
	if err != nil {
		return nil, err
	}
	statement, args, err := buildMySQLInsert(table, data)
	if err != nil {
		return nil, err
	}
	result, err := db.ExecContext(ctx, statement, args...)
	if err != nil {
		return nil, fmt.Errorf("insert row: %w", err)
	}
	keys, err := queryStrings(ctx, db, mysqlPrimaryKeyQuery, database, table)
	if err != nil {
		return nil, fmt.Errorf("read primary key: %w", err)
	}
	keyValues, known := insertedKey(keys, data, result)
	if !known {
		return &QueryResult{Command: "INSERT", AffectedRows: 1}, nil
	}
	return m.readRow(ctx, db, table, keys, keyValues)
}

// insertedKey is the new row's key values, when they can be known.
func insertedKey(keys []string, data map[string]interface{}, result sql.Result) ([]interface{}, bool) {
	if len(keys) == 0 {
		return nil, false
	}
	values := make([]interface{}, 0, len(keys))
	for _, key := range keys {
		value, sent := data[key]
		if !sent {
			break
		}
		converted, err := rowArg(value)
		if err != nil {
			return nil, false
		}
		values = append(values, converted)
	}
	if len(values) == len(keys) {
		return values, true
	}
	if len(keys) != 1 {
		return nil, false
	}
	generated, err := result.LastInsertId()
	if err != nil || generated == 0 {
		return nil, false
	}
	return []interface{}{generated}, true
}

func (m *MySQLCatalog) readRow(ctx context.Context, db *sql.DB, table string, keys []string, values []interface{}) (*QueryResult, error) {
	conditions := make([]string, len(keys))
	for index, key := range keys {
		conditions[index] = QuoteMySQLIdent(key) + " = ?"
	}
	rows, err := db.QueryContext(ctx, "SELECT * FROM "+QuoteMySQLIdent(table)+sqlWhere+strings.Join(conditions, " AND "), values...)
	if err != nil {
		return nil, fmt.Errorf("read inserted row: %w", err)
	}
	defer rows.Close()
	columns, found, err := scanQueryRows(rows)
	if err != nil {
		return nil, err
	}
	return &QueryResult{Columns: columns, Rows: found}, nil
}

// UpdateRow updates a row by primary key.
func (m *MySQLCatalog) UpdateRow(ctx context.Context, db *sql.DB, schemaName, table, keyColumn, keyValue string, data map[string]interface{}) error {
	if len(data) == 0 {
		return invalidInput("data is required")
	}
	if _, err := m.database(ctx, db, schemaName); err != nil {
		return err
	}
	keys := sortedKeys(data)
	assignments := make([]string, 0, len(keys))
	args := make([]interface{}, 0, len(keys)+1)
	for _, key := range keys {
		value, err := rowArg(data[key])
		if err != nil {
			return fmt.Errorf("column %q: %w", key, err)
		}
		assignments = append(assignments, QuoteMySQLIdent(key)+" = ?")
		args = append(args, value)
	}
	statement := "UPDATE " + QuoteMySQLIdent(table) + " SET " + strings.Join(assignments, ", ") +
		sqlWhere + QuoteMySQLIdent(keyColumn) + " = ?"
	if _, err := db.ExecContext(ctx, statement, append(args, keyValue)...); err != nil {
		return fmt.Errorf("update row: %w", err)
	}
	return nil
}

// DeleteRow deletes a row by primary key.
func (m *MySQLCatalog) DeleteRow(ctx context.Context, db *sql.DB, schemaName, table, keyColumn, keyValue string) error {
	if _, err := m.database(ctx, db, schemaName); err != nil {
		return err
	}
	statement := "DELETE FROM " + QuoteMySQLIdent(table) + sqlWhere + QuoteMySQLIdent(keyColumn) + " = ?"
	if _, err := db.ExecContext(ctx, statement, keyValue); err != nil {
		return fmt.Errorf("delete row: %w", err)
	}
	return nil
}

const mysqlPrimaryKeyQuery = `
SELECT column_name FROM information_schema.key_column_usage
WHERE table_schema = ? AND table_name = ? AND constraint_name = 'PRIMARY'
ORDER BY ordinal_position`
