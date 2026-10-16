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

const mysqlNoEndUserAuth = "end-user sign-up and sign-in are not available for MySQL projects yet: " +
	"the auth service keeps a project's users in Postgres (ADR 0041)"

// projectEngine reads the engine of the project's database from its row.
func projectEngine(ctx context.Context, c *call, projectID string) (string, error) {
	var project struct {
		DatabaseType string `json:"databaseType"`
	}
	if err := c.get(ctx, provisionAPI+projectID, nil, &project); err != nil {
		return "", err
	}
	if strings.EqualFold(project.DatabaseType, engineMySQL) {
		return engineMySQL, nil
	}
	return enginePostgres, nil
}

var (
	mysqlEnumValue = regexp.MustCompile(`'((?:[^']|'')*)'`)
	mysqlBaseType  = regexp.MustCompile(`^[a-z]+`)
)

// mysqlTSTypes maps MySQL base types; the API serves numbers as numbers, as
// the Postgres map does.
var mysqlTSTypes = map[string]string{
	"tinyint": "number", "smallint": "number", "mediumint": "number", "int": "number", "integer": "number",
	"bigint": "number", "decimal": "number", "numeric": "number", "float": "number", "double": "number",
	"real": "number", "bit": "number", "year": "number",
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
