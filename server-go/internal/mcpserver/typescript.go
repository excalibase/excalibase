package mcpserver

import (
	"fmt"
	"regexp"
	"strings"
)

var plainIdentifier = regexp.MustCompile(`^[A-Za-z_$][A-Za-z0-9_$]*$`)

// tsTypes maps information_schema data types to TypeScript.
var tsTypes = map[string]string{
	"smallint": "number", "integer": "number", "bigint": "number", "real": "number",
	"double precision": "number", "numeric": "number", "decimal": "number", "oid": "number",
	"boolean": "boolean",
	"json":    "Json", "jsonb": "Json",
	"ARRAY": "unknown[]",
}

// typeScriptFor renders one row interface per table and a Database map of a
// schema's tables, from the columns the schema routes report.
func typeScriptFor(schemaName string, tables []tableColumns) string {
	var out strings.Builder
	out.WriteString("// Generated from the project's database columns.\n")
	out.WriteString("export type Json = string | number | boolean | null | { [key: string]: Json } | Json[];\n")
	taken := map[string]bool{}
	names := make([]string, len(tables))
	for i, table := range tables {
		names[i] = uniqueName(pascalCase(table.Name), taken)
		fmt.Fprintf(&out, "\nexport interface %s {\n", names[i])
		for _, column := range table.Columns {
			fieldType := tsType(column.DataType)
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
