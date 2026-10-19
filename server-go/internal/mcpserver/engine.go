package mcpserver

import (
	"context"
	"regexp"
	"strconv"
	"strings"
)

// The engines a project's database runs on (ADR 0041).
const (
	enginePostgres = "POSTGRESQL"
	engineMySQL    = "MYSQL"
)

// projectEngine reads the engine of the project's database from its row.
func projectEngine(ctx context.Context, c *call, projectID string) (string, error) {
	engine, _, err := projectEngineAndDatabase(ctx, c, projectID)
	return engine, err
}

// projectEngineAndDatabase reads the engine and database name from the project's row.
func projectEngineAndDatabase(ctx context.Context, c *call, projectID string) (engine, database string, err error) {
	var project struct {
		DatabaseType string `json:"databaseType"`
		DatabaseName string `json:"databaseName"`
	}
	if err := c.get(ctx, provisionAPI+projectID, nil, &project); err != nil {
		return "", "", err
	}
	if strings.EqualFold(project.DatabaseType, engineMySQL) {
		return engineMySQL, project.DatabaseName, nil
	}
	return enginePostgres, project.DatabaseName, nil
}

var (
	mysqlEnumValue = regexp.MustCompile(`'((?:[^']|'')*)'`)
	mysqlBaseType  = regexp.MustCompile(`^[a-z]+`)
)

// mysqlTSTypes maps MySQL base types as the Postgres map does: bigints and
// decimals as strings of every digit, BIT as \x hex like binary.
var mysqlTSTypes = map[string]string{
	"tinyint": "number", "smallint": "number", "mediumint": "number", "int": "number", "integer": "number",
	"bigint": "string", "decimal": "string", "numeric": "string", "float": "number", "double": "number",
	"real": "number", "year": "number",
	"bool": "boolean", "boolean": "boolean",
	"json": "unknown",
}

// mysqlTSType maps one column's COLUMN_TYPE, e.g. tinyint(1) or enum('a','b').
func mysqlTSType(columnType string) string {
	lower := strings.ToLower(strings.TrimSpace(columnType))
	if strings.HasPrefix(lower, "tinyint(1)") {
		return "boolean"
	}
	base := mysqlBaseType.FindString(lower)
	if base == "enum" {
		return mysqlEnumUnion(columnType)
	}
	if mapped, ok := mysqlTSTypes[base]; ok {
		return mapped
	}
	return "string"
}

func mysqlEnumUnion(columnType string) string {
	matches := mysqlEnumValue.FindAllStringSubmatch(columnType, -1)
	if len(matches) == 0 {
		return "string"
	}
	values := make([]string, len(matches))
	for index, match := range matches {
		values[index] = strconv.Quote(strings.ReplaceAll(match[1], "''", "'"))
	}
	return strings.Join(values, " | ")
}
