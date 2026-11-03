// Package graphqlnames names what the engine (excalibase-graphql) generates
// from a schema: root fields and the relationship fields foreign keys add.
// MCP describes these names and permission checks refuse any other.
package graphqlnames

import (
	"strings"
	"unicode"
)

// LowerCamel is NamingUtils.toLowerCamelCase, quirks included: the first
// letter is lowered even right after a leading underscore.
func LowerCamel(name string) string {
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

// Pascal is NamingUtils.capitalize.
func Pascal(name string) string {
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
