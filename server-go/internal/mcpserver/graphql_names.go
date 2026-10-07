package mcpserver

import (
	"strings"
	"unicode"
)

// rootFields are the GraphQL root fields the engine generates for one table,
// named by its rules (excalibase-graphql NamingUtils.fieldNameOf/typeNameOf
// with the schema-qualified key, GraphqlConstants for the suffixes). Which of
// them a role sees depends on its permissions.
type rootFields struct {
	Type         string `json:"type"`
	Query        string `json:"query"`
	Connection   string `json:"connection"`
	Aggregate    string `json:"aggregate"`
	Subscription string `json:"subscription"`
	Insert       string `json:"insert,omitempty"`
	InsertMany   string `json:"insertMany,omitempty"`
	Update       string `json:"update,omitempty"`
	Delete       string `json:"delete,omitempty"`
}

// graphQLFields names a table's root fields; a view has no mutations.
func graphQLFields(schemaName, table string, view bool) rootFields {
	field := lowerCamel(schemaName) + pascal(table)
	typeName := pascal(schemaName) + pascal(table)
	fields := rootFields{
		Type: typeName, Query: field, Connection: field + "Connection",
		Aggregate: field + "Aggregate", Subscription: field + "Changes",
	}
	if !view {
		fields.Insert, fields.InsertMany = "create"+typeName, "createMany"+typeName
		fields.Update, fields.Delete = "update"+typeName, "delete"+typeName
	}
	return fields
}

// functionField is a tracked function's root field, from its schema.name key.
func functionField(key string) string {
	schemaName, name, _ := strings.Cut(key, ".")
	return lowerCamel(schemaName) + pascal(name)
}

// lowerCamel is NamingUtils.toLowerCamelCase, quirks included: the first
// letter is lowered even right after a leading underscore.
func lowerCamel(name string) string {
	var out strings.Builder
	upperNext, first := false, true
	for _, ch := range name {
		switch {
		case ch == '_':
			upperNext = true
		case first:
			out.WriteRune(unicode.ToLower(ch))
			first = false
		case upperNext:
			out.WriteRune(unicode.ToUpper(ch))
			upperNext = false
		default:
			out.WriteRune(ch)
		}
	}
	return out.String()
}

// pascal is NamingUtils.capitalize.
func pascal(name string) string {
	var out strings.Builder
	upperNext := true
	for _, ch := range name {
		switch {
		case ch == '_':
			upperNext = true
		case upperNext:
			out.WriteRune(unicode.ToUpper(ch))
			upperNext = false
		default:
			out.WriteRune(ch)
		}
	}
	return out.String()
}
