package schema

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// mysqlTable is a table as the catalogue names it. Row SQL is built from
// these names, never from the request's, so an unknown table or column is a
// 400 and no request text becomes an identifier.
type mysqlTable struct {
	name    string
	columns map[string]string
}

// column is a column's quoted catalogue name.
func (t mysqlTable) column(requested string) (string, error) {
	name, ok := t.columns[requested]
	if !ok {
		return "", invalidInput("table %s has no column %q", t.name, requested)
	}
	return QuoteMySQLIdent(name), nil
}

func (t mysqlTable) quoted() string { return QuoteMySQLIdent(t.name) }

// tableOf reads a table and its columns from the project's database.
func (m *MySQLCatalog) tableOf(ctx context.Context, db *sql.DB, schemaName, table string) (mysqlTable, string, error) {
	database, err := m.database(ctx, db, schemaName)
	if err != nil {
		return mysqlTable{}, "", err
	}
	rows, err := db.QueryContext(ctx, `SELECT table_name, column_name FROM information_schema.columns
WHERE table_schema = ? AND table_name = ?`, database, table)
	if err != nil {
		return mysqlTable{}, "", fmt.Errorf("read table: %w", err)
	}
	defer rows.Close()
	found := mysqlTable{columns: map[string]string{}}
	for rows.Next() {
		var column string
		if err := rows.Scan(&found.name, &column); err != nil {
			return mysqlTable{}, "", fmt.Errorf("read table: %w", err)
		}
		found.columns[column] = column
	}
	if err := rows.Err(); err != nil {
		return mysqlTable{}, "", fmt.Errorf("read table: %w", err)
	}
	if found.name == "" {
		return mysqlTable{}, "", invalidInput("no table %q", table)
	}
	return found, database, nil
}

// GetRows returns a page of a table's rows.
func (m *MySQLCatalog) GetRows(ctx context.Context, db *sql.DB, schemaName, table string, opts RowQueryOpts) (*RowsResult, error) {
	target, _, err := m.tableOf(ctx, db, schemaName, table)
	if err != nil {
		return nil, err
	}
	dataSQL, countSQL, dataArgs, countArgs, err := buildMySQLSelect(target, applyRowDefaults(opts))
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

func buildMySQLSelect(target mysqlTable, opts RowQueryOpts) (dataSQL, countSQL string, dataArgs, countArgs []interface{}, err error) {
	where, countArgs, err := mysqlWhere(target, opts.Filters)
	if err != nil {
		return "", "", nil, nil, err
	}
	from := " FROM " + target.quoted() + where
	countSQL = "SELECT COUNT(*)" + from
	dataSQL = "SELECT *" + from
	if opts.Sort != "" {
		order, err := mysqlOrder(target, opts)
		if err != nil {
			return "", "", nil, nil, err
		}
		dataSQL += order
	}
	dataSQL += " LIMIT ? OFFSET ?"
	dataArgs = append(append([]interface{}{}, countArgs...), opts.Limit, opts.Offset)
	return dataSQL, countSQL, dataArgs, countArgs, nil
}

func mysqlOrder(target mysqlTable, opts RowQueryOpts) (string, error) {
	column, err := target.column(opts.Sort)
	if err != nil {
		return "", err
	}
	direction := "ASC"
	if opts.Order != "" {
		if err := validateSortOrder(opts.Order); err != nil {
			return "", err
		}
		direction = strings.ToUpper(opts.Order)
	}
	return " ORDER BY " + column + " " + direction, nil
}

// mysqlWhere is the filters as MySQL takes them; ilike compares lower case,
// whatever the column's collation.
func mysqlWhere(target mysqlTable, filters []RowFilter) (string, []interface{}, error) {
	clauses := make([]string, 0, len(filters))
	var args []interface{}
	for _, filter := range filters {
		operator := strings.ToLower(filter.Operator)
		sqlOperator, ok := allowedOperators[operator]
		if !ok {
			return "", nil, invalidInput("invalid operator: %q", filter.Operator)
		}
		column, err := target.column(filter.Column)
		if err != nil {
			return "", nil, err
		}
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

// assignments are "column = ?" for each sent column, with their values.
func assignments(target mysqlTable, data map[string]interface{}) ([]string, []interface{}, error) {
	keys := sortedKeys(data)
	columns := make([]string, 0, len(keys))
	args := make([]interface{}, 0, len(keys))
	for _, key := range keys {
		column, err := target.column(key)
		if err != nil {
			return nil, nil, err
		}
		value, err := rowArg(data[key])
		if err != nil {
			return nil, nil, fmt.Errorf("column %q: %w", key, err)
		}
		columns = append(columns, column)
		args = append(args, value)
	}
	return columns, args, nil
}

func buildMySQLInsert(target mysqlTable, data map[string]interface{}) (string, []interface{}, error) {
	columns, args, err := assignments(target, data)
	if err != nil {
		return "", nil, err
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?, ", len(columns)), ", ")
	return "INSERT INTO " + target.quoted() + " (" + strings.Join(columns, ", ") + ") VALUES (" + placeholders + ")", args, nil
}

// InsertRow inserts a row and reads it back by its primary key: the values
// sent, or the AUTO_INCREMENT id MySQL generated. A row whose key cannot be
// known (no key, or one filled by an expression) is reported as inserted
// without being read back.
func (m *MySQLCatalog) InsertRow(ctx context.Context, db *sql.DB, schemaName, table string, data map[string]interface{}) (*QueryResult, error) {
	target, database, err := m.tableOf(ctx, db, schemaName, table)
	if err != nil {
		return nil, err
	}
	statement, args, err := buildMySQLInsert(target, data)
	if err != nil {
		return nil, err
	}
	result, err := db.ExecContext(ctx, statement, args...)
	if err != nil {
		return nil, fmt.Errorf("insert row: %w", err)
	}
	keys, err := queryStrings(ctx, db, mysqlPrimaryKeyQuery, database, target.name)
	if err != nil {
		return nil, fmt.Errorf("read primary key: %w", err)
	}
	keyValues, known := insertedKey(keys, data, result)
	if !known {
		return &QueryResult{Command: "INSERT", AffectedRows: 1}, nil
	}
	return readRow(ctx, db, target, keys, keyValues)
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

func readRow(ctx context.Context, db *sql.DB, target mysqlTable, keys []string, values []interface{}) (*QueryResult, error) {
	query, err := buildMySQLReadBack(target, keys)
	if err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(ctx, query, values...)
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

func buildMySQLReadBack(target mysqlTable, keys []string) (string, error) {
	conditions := make([]string, len(keys))
	for index, key := range keys {
		column, err := target.column(key)
		if err != nil {
			return "", err
		}
		conditions[index] = column + " = ?"
	}
	return "SELECT * FROM " + target.quoted() + sqlWhere + strings.Join(conditions, " AND "), nil
}

// UpdateRow updates a row by primary key.
func (m *MySQLCatalog) UpdateRow(ctx context.Context, db *sql.DB, schemaName, table, keyColumn, keyValue string, data map[string]interface{}) error {
	if len(data) == 0 {
		return invalidInput("data is required")
	}
	target, _, err := m.tableOf(ctx, db, schemaName, table)
	if err != nil {
		return err
	}
	statement, args, err := buildMySQLUpdate(target, data, keyColumn)
	if err != nil {
		return err
	}
	if _, err := db.ExecContext(ctx, statement, append(args, keyValue)...); err != nil {
		return fmt.Errorf("update row: %w", err)
	}
	return nil
}

func buildMySQLUpdate(target mysqlTable, data map[string]interface{}, keyColumn string) (string, []interface{}, error) {
	columns, args, err := assignments(target, data)
	if err != nil {
		return "", nil, err
	}
	key, err := target.column(keyColumn)
	if err != nil {
		return "", nil, err
	}
	return "UPDATE " + target.quoted() + " SET " + strings.Join(columns, " = ?, ") + " = ?" + sqlWhere + key + " = ?", args, nil
}

// DeleteRow deletes a row by primary key.
func (m *MySQLCatalog) DeleteRow(ctx context.Context, db *sql.DB, schemaName, table, keyColumn, keyValue string) error {
	target, _, err := m.tableOf(ctx, db, schemaName, table)
	if err != nil {
		return err
	}
	statement, err := buildMySQLDelete(target, keyColumn)
	if err != nil {
		return err
	}
	if _, err := db.ExecContext(ctx, statement, keyValue); err != nil {
		return fmt.Errorf("delete row: %w", err)
	}
	return nil
}

func buildMySQLDelete(target mysqlTable, keyColumn string) (string, error) {
	key, err := target.column(keyColumn)
	if err != nil {
		return "", err
	}
	return "DELETE FROM " + target.quoted() + sqlWhere + key + " = ?", nil
}

const mysqlPrimaryKeyQuery = `
SELECT column_name FROM information_schema.key_column_usage
WHERE table_schema = ? AND table_name = ? AND constraint_name = 'PRIMARY'
ORDER BY ordinal_position`
