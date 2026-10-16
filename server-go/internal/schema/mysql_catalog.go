package schema

import (
	"context"
	"database/sql"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"
)

// MySQLCatalog is the schema browser for a MySQL project (ADR 0041, EXC-619).
// A MySQL project has one database and no schemas inside it, so the schema
// a request names must be public (Studio's default) or that database.
type MySQLCatalog struct {
	statementTimeout time.Duration
}

func NewMySQLCatalog() *MySQLCatalog {
	return &MySQLCatalog{statementTimeout: defaultStatementTimeout}
}

// WithStatementTimeout bounds every statement the SQL runner executes.
func (m *MySQLCatalog) WithStatementTimeout(d time.Duration) *MySQLCatalog {
	m.statementTimeout = d
	return m
}

// database is the project's database when schemaName names it.
func (m *MySQLCatalog) database(ctx context.Context, db *sql.DB, schemaName string) (string, error) {
	var current sql.NullString
	if err := db.QueryRowContext(ctx, "SELECT DATABASE()").Scan(&current); err != nil {
		return "", fmt.Errorf("read database: %w", err)
	}
	if !current.Valid {
		return "", fmt.Errorf("the connection has no database")
	}
	if schemaName == "" || schemaName == "public" || schemaName == current.String {
		return current.String, nil
	}
	return "", invalidInput("a MySQL project has one database, %s; there is no schema %q", current.String, schemaName)
}

func (m *MySQLCatalog) TestConnection(ctx context.Context, db *sql.DB) bool {
	return db.PingContext(ctx) == nil
}

func (m *MySQLCatalog) GetTables(ctx context.Context, db *sql.DB, schemaName string) ([]TableInfo, error) {
	database, err := m.database(ctx, db, schemaName)
	if err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(ctx, mysqlTablesQuery, database)
	if err != nil {
		return nil, fmt.Errorf("query tables: %w", err)
	}
	defer rows.Close()
	tables := make([]TableInfo, 0)
	for rows.Next() {
		var table TableInfo
		if err := rows.Scan(&table.Name, &table.Schema, &table.Type, &table.Comment); err != nil {
			return nil, fmt.Errorf("scan table: %w", err)
		}
		tables = append(tables, table)
	}
	return tables, rows.Err()
}

// GetColumns reports each column's full MySQL type (tinyint(1), enum('a','b'))
// as its data type; an AUTO_INCREMENT column reports that as its default.
func (m *MySQLCatalog) GetColumns(ctx context.Context, db *sql.DB, schemaName, table string) ([]ColumnInfo, error) {
	database, err := m.database(ctx, db, schemaName)
	if err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(ctx, mysqlColumnsQuery, database, table)
	if err != nil {
		return nil, fmt.Errorf("query columns: %w", err)
	}
	defer rows.Close()
	columns := make([]ColumnInfo, 0)
	for rows.Next() {
		var column ColumnInfo
		var nullable, extra string
		if err := rows.Scan(&column.Name, &column.DataType, &nullable, &column.DefaultValue, &column.OrdinalPosition,
			&column.CharacterMaximumLength, &column.NumericPrecision, &column.NumericScale,
			&column.PrimaryKey, &column.Unique, &column.Comment, &extra); err != nil {
			return nil, fmt.Errorf("scan column: %w", err)
		}
		column.Nullable = nullable == "YES"
		if column.DefaultValue == nil && strings.Contains(strings.ToLower(extra), "auto_increment") {
			autoIncrement := "AUTO_INCREMENT"
			column.DefaultValue = &autoIncrement
		}
		columns = append(columns, column)
	}
	return columns, rows.Err()
}

func (m *MySQLCatalog) GetRelationships(ctx context.Context, db *sql.DB, schemaName string) ([]RelationshipInfo, error) {
	database, err := m.database(ctx, db, schemaName)
	if err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(ctx, mysqlRelationshipsQuery, database)
	if err != nil {
		return nil, fmt.Errorf("query relationships: %w", err)
	}
	defer rows.Close()
	relationships := make([]RelationshipInfo, 0)
	for rows.Next() {
		var relationship RelationshipInfo
		if err := rows.Scan(&relationship.ConstraintName, &relationship.SourceTable, &relationship.SourceColumn,
			&relationship.TargetTable, &relationship.TargetColumn, &relationship.OnDelete, &relationship.OnUpdate); err != nil {
			return nil, fmt.Errorf("scan relationship: %w", err)
		}
		relationships = append(relationships, relationship)
	}
	return relationships, rows.Err()
}

func (m *MySQLCatalog) GetIndexes(ctx context.Context, db *sql.DB, schemaName, table string) ([]IndexInfo, error) {
	database, err := m.database(ctx, db, schemaName)
	if err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(ctx, mysqlIndexesQuery, database, table)
	if err != nil {
		return nil, fmt.Errorf("query indexes: %w", err)
	}
	defer rows.Close()
	indexes := make([]IndexInfo, 0)
	for rows.Next() {
		var index IndexInfo
		var columns string
		if err := rows.Scan(&index.Name, &index.TableName, &columns, &index.Unique, &index.Type); err != nil {
			return nil, fmt.Errorf("scan index: %w", err)
		}
		index.Columns = strings.Split(columns, indexColumnSeparator)
		indexes = append(indexes, index)
	}
	return indexes, rows.Err()
}

// backtickedName is a column as MySQL prints it inside a CHECK clause.
var backtickedName = regexp.MustCompile("`((?:[^`]|``)+)`")

func (m *MySQLCatalog) GetCheckConstraints(ctx context.Context, db *sql.DB, schemaName, table string) ([]CheckConstraint, error) {
	database, err := m.database(ctx, db, schemaName)
	if err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(ctx, mysqlChecksQuery, database, table)
	if err != nil {
		return nil, fmt.Errorf("query check constraints: %w", err)
	}
	defer rows.Close()
	checks := make([]CheckConstraint, 0)
	for rows.Next() {
		check := CheckConstraint{Columns: []string{}}
		if err := rows.Scan(&check.Name, &check.Definition); err != nil {
			return nil, fmt.Errorf("scan check constraint: %w", err)
		}
		for _, match := range backtickedName.FindAllStringSubmatch(check.Definition, -1) {
			name := strings.ReplaceAll(match[1], "``", "`")
			if !slices.Contains(check.Columns, name) {
				check.Columns = append(check.Columns, name)
			}
		}
		checks = append(checks, check)
	}
	return checks, rows.Err()
}

const mysqlTablesQuery = `
SELECT table_name, table_schema, table_type, NULLIF(table_comment, '')
FROM information_schema.tables
WHERE table_schema = ? AND table_type IN ('BASE TABLE', 'VIEW')
ORDER BY table_name`

// mysqlColumnsQuery flags a column unique only when a unique index covers it
// alone, as the Postgres query does (EXC-598).
const mysqlColumnsQuery = `
SELECT c.column_name, c.column_type, c.is_nullable, c.column_default, c.ordinal_position,
       c.character_maximum_length, c.numeric_precision, c.numeric_scale,
       c.column_key = 'PRI',
       EXISTS (
           SELECT 1 FROM information_schema.statistics s
           WHERE s.table_schema = c.table_schema AND s.table_name = c.table_name
             AND s.column_name = c.column_name AND s.non_unique = 0 AND s.index_name <> 'PRIMARY'
             AND (SELECT COUNT(*) FROM information_schema.statistics w
                  WHERE w.table_schema = s.table_schema AND w.table_name = s.table_name
                    AND w.index_name = s.index_name) = 1
       ),
       NULLIF(c.column_comment, ''), c.extra
FROM information_schema.columns c
WHERE c.table_schema = ? AND c.table_name = ?
ORDER BY c.ordinal_position`

const mysqlRelationshipsQuery = `
SELECT k.constraint_name, k.table_name, k.column_name, k.referenced_table_name, k.referenced_column_name,
       r.delete_rule, r.update_rule
FROM information_schema.key_column_usage k
JOIN information_schema.referential_constraints r
  ON r.constraint_schema = k.constraint_schema AND r.constraint_name = k.constraint_name AND r.table_name = k.table_name
WHERE k.table_schema = ? AND k.referenced_table_name IS NOT NULL
ORDER BY k.constraint_name, k.ordinal_position`

// mysqlIndexesQuery joins columns with the unit separator, which no column
// name carries; a functional key part reports its expression.
const mysqlIndexesQuery = `
SELECT index_name, table_name,
       GROUP_CONCAT(COALESCE(column_name, expression) ORDER BY seq_in_index SEPARATOR '` + indexColumnSeparator + `'),
       MIN(non_unique) = 0, LOWER(MIN(index_type))
FROM information_schema.statistics
WHERE table_schema = ? AND table_name = ?
GROUP BY index_name, table_name
ORDER BY index_name`

const indexColumnSeparator = "\x1f"

const mysqlChecksQuery = `
SELECT cc.constraint_name, cc.check_clause
FROM information_schema.table_constraints tc
JOIN information_schema.check_constraints cc
  ON cc.constraint_schema = tc.constraint_schema AND cc.constraint_name = tc.constraint_name
WHERE tc.table_schema = ? AND tc.table_name = ? AND tc.constraint_type = 'CHECK'
ORDER BY cc.constraint_name`
