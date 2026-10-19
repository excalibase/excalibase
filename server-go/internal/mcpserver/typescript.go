package mcpserver

import (
	"fmt"
	"regexp"
	"strings"
)

var plainIdentifier = regexp.MustCompile(`^[A-Za-z_$][A-Za-z0-9_$]*$`)

// tsTypes maps information_schema data types to TypeScript. Bigints and
// decimals are strings: the API answers them as strings of every digit.
var tsTypes = map[string]string{
	"smallint": "number", "integer": "number", "bigint": "string", "real": "number",
	"double precision": "number", "numeric": "string", "decimal": "string", "oid": "number",
	"boolean": "boolean",
	"json":    "Json", "jsonb": "Json",
}

// typeScriptFor renders a Postgres schema's types.
func typeScriptFor(schemaName string, tables []tableColumns) string {
	return typeScriptForEngine(schemaName, tables, enginePostgres)
}

// typeScriptForEngine renders one row interface per table and a Database map
// of a schema's tables, from the columns the schema routes report.
func typeScriptForEngine(schemaName string, tables []tableColumns, engine string) string {
	columnType := tsType
	if engine == engineMySQL {
		columnType = mysqlTSType
	}
	var out strings.Builder
	out.WriteString("// Generated from the project's database columns.\n")
	if engine != engineMySQL {
		out.WriteString("export type Json = string | number | boolean | null | { [key: string]: Json } | Json[];\n")
	}
	taken := map[string]bool{}
	names := make([]string, len(tables))
	for i, table := range tables {
		names[i] = uniqueName(pascalCase(table.Name), taken)
		fmt.Fprintf(&out, "\nexport interface %s {\n", names[i])
		for _, column := range table.Columns {
			fieldType := columnType(column.DataType)
			if column.DataType == "ARRAY" {
				fieldType = arrayTSType(column.ElementType)
			}
			if column.Nullable {
				fieldType += " | null"
			}
			fmt.Fprintf(&out, "  %s: %s;\n", propertyName(column.Name), fieldType)
		}
		out.WriteString("}\n")
	}
	fmt.Fprintf(&out, "\nexport interface Database {\n  %s: {\n", propertyName(schemaName))
	for i, table := range tables {
		fmt.Fprintf(&out, "    %s: %s;\n", propertyName(table.Name), names[i])
	}
	out.WriteString("  };\n}\n")
	return out.String()
}

// arrayTSType is an array of its elements' type; unknown[] when the element
// type was not reported.
func arrayTSType(elementType string) string {
	if elementType == "" {
		return "unknown[]"
	}
	return tsType(elementType) + "[]"
}

func tsType(dataType string) string {
	if mapped, ok := tsTypes[dataType]; ok {
		return mapped
	}
	return "string"
}

func propertyName(name string) string {
	if plainIdentifier.MatchString(name) {
		return name
	}
	return fmt.Sprintf("%q", name)
}

func pascalCase(name string) string {
	var out strings.Builder
	upper := true
	for _, r := range name {
		isWordRune := r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9'
		if !isWordRune {
			upper = true
			continue
		}
		if upper && r >= 'a' && r <= 'z' {
			r -= 'a' - 'A'
		}
		upper = false
		out.WriteRune(r)
	}
	result := out.String()
	if result == "" || result[0] >= '0' && result[0] <= '9' {
		result = "T" + result
	}
	return result
}

func uniqueName(base string, taken map[string]bool) string {
	name := base
	for n := 2; taken[name]; n++ {
		name = fmt.Sprintf("%s%d", base, n)
	}
	taken[name] = true
	return name
}
