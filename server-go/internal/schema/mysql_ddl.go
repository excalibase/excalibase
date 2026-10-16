package schema

import (
	"context"
	"database/sql"
	"fmt"
	"regexp"
	"strings"
)

// QuoteMySQLIdent quotes a MySQL identifier with backticks.
func QuoteMySQLIdent(name string) string {
	return "`" + strings.ReplaceAll(name, "`", "``") + "`"
}

// quoteMySQLString quotes a literal for MySQL's default sql_mode, where a
// backslash escapes.
func quoteMySQLString(value string) string {
	return "'" + strings.NewReplacer(`\`, `\\`, `'`, `''`).Replace(value) + "'"
}

// mysqlLiteral is a quoted string without backslashes, so it reads the same
// whatever the session's escape mode.
const mysqlLiteral = `'(?:[^'\\]|'')*'`

// mysqlTypes is what a column may be declared as from the table editor; SQL
// covers everything else.
var mysqlTypes = regexp.MustCompile(`(?i)^(?:` +
	`(?:tinyint|smallint|mediumint|int|integer|bigint)(?:\(\d{1,3}\))?(?: unsigned)?(?: zerofill)?(?: auto_increment)?` +
	`|(?:decimal|numeric|dec|fixed)(?:\(\d{1,2}(?:, ?\d{1,2})?\))?(?: unsigned)?` +
	`|(?:float|double|real)(?:\(\d{1,2}(?:, ?\d{1,2})?\))?(?: unsigned)?` +
	`|bit(?:\(\d{1,2}\))?|bool|boolean|serial` +
	`|(?:char|varchar|binary|varbinary)\(\d{1,5}\)` +
	`|tinytext|text|mediumtext|longtext|tinyblob|blob|mediumblob|longblob|json|date|year|geometry|point` +
	`|(?:datetime|timestamp|time)(?:\([0-6]\))?` +
	`|(?:enum|set)\(` + mysqlLiteral + `(?:, ?` + mysqlLiteral + `)*\)` +
	`)$`)

// ValidateMySQLType accepts a column type the table editor may send.
func ValidateMySQLType(columnType string) error {
	if !mysqlTypes.MatchString(strings.TrimSpace(columnType)) {
		return invalidInput("invalid MySQL column type %q", columnType)
	}
	return nil
}

var (
	mysqlDefaultLiteral = regexp.MustCompile(`^(?:-?\d+(?:\.\d+)?|` + mysqlLiteral + `)$`)
	mysqlDefaultKeyword = regexp.MustCompile(`(?i)^(?:null|true|false|current_timestamp(?:\([0-6]?\))?)$`)
	mysqlDefaultCall    = map[string]bool{
		"(uuid())": true, "(uuid_to_bin(uuid()))": true, "(current_date)": true, "(curdate())": true, "(now())": true,
	}
)

// ValidateMySQLDefault accepts a literal or one of the expressions the table
// editor offers, and returns it as written.
func ValidateMySQLDefault(expression string) (string, error) {
	trimmed := strings.TrimSpace(expression)
	if mysqlDefaultLiteral.MatchString(trimmed) || mysqlDefaultKeyword.MatchString(trimmed) ||
		mysqlDefaultCall[strings.ToLower(trimmed)] {
		return trimmed, nil
	}
	return "", invalidInput("default %q is not a literal, CURRENT_TIMESTAMP or (UUID())", expression)
}

func buildMySQLCreateTable(req CreateTableRequest) (string, error) {
	if err := CheckNewName("table", req.Name); err != nil {
		return "", err
	}
	if len(req.Columns) == 0 {
		return "", invalidInput("a MySQL table needs at least one column")
	}
	definitions := make([]string, 0, len(req.Columns)+1)
	var keys []string
	for _, column := range req.Columns {
		definition, err := mysqlColumnDefinition(column.Name, column.Type, column.Nullable, column.Default, column.Unique)
		if err != nil {
			return "", err
		}
		definitions = append(definitions, definition)
		if column.PrimaryKey {
			keys = append(keys, QuoteMySQLIdent(column.Name))
		}
	}
	if len(keys) > 0 {
		definitions = append(definitions, "PRIMARY KEY ("+strings.Join(keys, ", ")+")")
	}
	statement := "CREATE TABLE " + QuoteMySQLIdent(req.Name) + " (" + strings.Join(definitions, ", ") + ")"
	if req.Comment != "" {
		statement += " COMMENT = " + quoteMySQLString(req.Comment)
	}
	return statement, nil
}

func mysqlColumnDefinition(name, columnType string, nullable bool, defaultValue *string, unique bool) (string, error) {
	if err := CheckNewName("column", name); err != nil {
		return "", err
	}
	if err := ValidateMySQLType(columnType); err != nil {
		return "", fmt.Errorf("column %q: %w", name, err)
	}
	definition := QuoteMySQLIdent(name) + " " + strings.TrimSpace(columnType)
	if !nullable {
		definition += " NOT NULL"
	}
	if defaultValue != nil {
		safe, err := ValidateMySQLDefault(*defaultValue)
		if err != nil {
			return "", fmt.Errorf("column %q: %w", name, err)
		}
		definition += " DEFAULT " + safe
	}
	if unique {
		definition += " UNIQUE"
	}
	return definition, nil
}

func buildMySQLIndex(req CreateIndexRequest) (string, error) {
	if err := checkNameLength("index", req.Name); err != nil {
		return "", err
	}
	if len(req.Columns) == 0 {
		return "", invalidInput("at least one column is required")
	}
	method := strings.ToUpper(req.Type)
	if method == "" {
		method = "BTREE"
	}
	if method != "BTREE" && method != "HASH" {
		return "", invalidInput("MySQL indexes here are btree or hash; create FULLTEXT or SPATIAL ones with SQL")
	}
	columns := make([]string, len(req.Columns))
	for index, column := range req.Columns {
		columns[index] = QuoteMySQLIdent(column)
	}
	statement := "CREATE "
	if req.Unique {
		statement += "UNIQUE "
	}
	return statement + "INDEX " + QuoteMySQLIdent(req.Name) + " ON " + QuoteMySQLIdent(req.Table) +
		" (" + strings.Join(columns, ", ") + ") USING " + method, nil
}

// CreateTable creates a table in the project's database.
func (m *MySQLCatalog) CreateTable(ctx context.Context, db *sql.DB, req CreateTableRequest) error {
	if _, err := m.database(ctx, db, req.Schema); err != nil {
		return err
	}
	statement, err := buildMySQLCreateTable(req)
	if err != nil {
		return err
	}
	return m.exec(ctx, db, "create table", statement)
}

// DropTable drops a table; MySQL's CASCADE does nothing, so it is not sent.
func (m *MySQLCatalog) DropTable(ctx context.Context, db *sql.DB, schemaName, name string, _ bool) error {
	if _, err := m.database(ctx, db, schemaName); err != nil {
		return err
	}
	return m.exec(ctx, db, "drop table", "DROP TABLE "+QuoteMySQLIdent(name))
}

// AddColumn adds a column to a table.
func (m *MySQLCatalog) AddColumn(ctx context.Context, db *sql.DB, schemaName, table string, req AddColumnRequest) error {
	if _, err := m.database(ctx, db, schemaName); err != nil {
		return err
	}
	definition, err := mysqlColumnDefinition(req.Name, req.Type, req.Nullable, req.Default, req.Unique)
	if err != nil {
		return err
	}
	return m.exec(ctx, db, "add column", "ALTER TABLE "+QuoteMySQLIdent(table)+" ADD COLUMN "+definition)
}

// DropColumn removes a column from a table.
func (m *MySQLCatalog) DropColumn(ctx context.Context, db *sql.DB, schemaName, table, column string) error {
	if _, err := m.database(ctx, db, schemaName); err != nil {
		return err
	}
	return m.exec(ctx, db, "drop column", "ALTER TABLE "+QuoteMySQLIdent(table)+" DROP COLUMN "+QuoteMySQLIdent(column))
}

// CreateIndex creates an index on a table.
func (m *MySQLCatalog) CreateIndex(ctx context.Context, db *sql.DB, req CreateIndexRequest) error {
	if _, err := m.database(ctx, db, req.Schema); err != nil {
		return err
	}
	statement, err := buildMySQLIndex(req)
	if err != nil {
		return err
	}
	return m.exec(ctx, db, "create index", statement)
}

// DropIndex drops an index by name. MySQL names an index per table, so the
// table is found from the name, and a name on several tables is refused.
func (m *MySQLCatalog) DropIndex(ctx context.Context, db *sql.DB, schemaName, name string) error {
	database, err := m.database(ctx, db, schemaName)
	if err != nil {
		return err
	}
	tables, err := queryStrings(ctx, db, `SELECT DISTINCT table_name FROM information_schema.statistics
WHERE table_schema = ? AND index_name = ?`, database, name)
	if err != nil {
		return fmt.Errorf("find index: %w", err)
	}
	switch len(tables) {
	case 0:
		return invalidInput("no index %q", name)
	case 1:
		return m.exec(ctx, db, "drop index", "DROP INDEX "+QuoteMySQLIdent(name)+" ON "+QuoteMySQLIdent(tables[0]))
	}
	return invalidInput("index %q exists on several tables (%s); drop it with SQL", name, strings.Join(tables, ", "))
}

func (m *MySQLCatalog) exec(ctx context.Context, db *sql.DB, action, statement string) error {
	if _, err := db.ExecContext(ctx, statement); err != nil {
		return fmt.Errorf("%s: %w", action, err)
	}
	return nil
}

func queryStrings(ctx context.Context, db *sql.DB, query string, args ...interface{}) ([]string, error) {
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var values []string
	for rows.Next() {
		var value string
		if err := rows.Scan(&value); err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, rows.Err()
}
