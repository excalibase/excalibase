package schema

import (
	"fmt"
	"regexp"
	"strings"
)

// QuoteIdent quotes a PostgreSQL identifier (table name, column name, etc.)
// by wrapping it in double quotes and escaping embedded double quotes.
func QuoteIdent(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}

// QuoteLiteral quotes a PostgreSQL string literal by wrapping it in single
// quotes and escaping embedded single quotes.
func QuoteLiteral(val string) string {
	return `'` + strings.ReplaceAll(val, `'`, `''`) + `'`
}

// ValidatePolicyCommand validates that the command is a valid PostgreSQL RLS command.
func ValidatePolicyCommand(cmd string) error {
	switch strings.ToUpper(cmd) {
	case "ALL", "SELECT", "INSERT", "UPDATE", "DELETE":
		return nil
	default:
		return fmt.Errorf("invalid policy command: %q", cmd)
	}
}

// ValidateLanguage validates that the language is a valid PostgreSQL function language.
func ValidateLanguage(lang string) error {
	switch strings.ToLower(lang) {
	case "sql", "plpgsql", "plpython3u", "plperl", "plperlu", "pltcl", "pltclu":
		return nil
	default:
		return fmt.Errorf("invalid language: %q", lang)
	}
}

// ValidateVolatility validates that the volatility is a valid PostgreSQL function volatility.
func ValidateVolatility(vol string) error {
	switch strings.ToUpper(vol) {
	case "VOLATILE", "STABLE", "IMMUTABLE":
		return nil
	default:
		return fmt.Errorf("invalid volatility: %q", vol)
	}
}

// pgTypePattern matches valid PG type names: alphanumeric, spaces, parens, brackets, commas.
// Rejects semicolons, quotes, comments.
var pgTypePattern = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_ ()\[\],]*$`)

// ValidateTypeName validates that a PostgreSQL type name doesn't contain injection characters.
func ValidateTypeName(typeName string) error {
	if typeName == "" {
		return fmt.Errorf("type name is required")
	}
	if !pgTypePattern.MatchString(typeName) {
		return fmt.Errorf("invalid type name: %q", typeName)
	}
	return nil
}

// pgArgTypesPattern matches valid PG function argument type lists (can be empty).
var pgArgTypesPattern = regexp.MustCompile(`^[a-zA-Z0-9_ ()\[\],]*$`)

// ValidateArgTypes validates function argument types string.
func ValidateArgTypes(argTypes string) error {
	if argTypes == "" {
		return nil
	}
	if !pgArgTypesPattern.MatchString(argTypes) {
		return fmt.Errorf("invalid argument types: %q", argTypes)
	}
	return nil
}

// columnNamePattern matches valid PostgreSQL column names: starts with letter/underscore,
// followed by alphanumeric/underscore. No spaces, parens, or special chars.
var columnNamePattern = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*$`)

// ValidateColumnName validates that a column name is a safe identifier.
func ValidateColumnName(name string) error {
	if name == "" {
		return fmt.Errorf("column name is required")
	}
	if !columnNamePattern.MatchString(name) {
		return fmt.Errorf("invalid column name: %q", name)
	}
	return nil
}

// schemaNamePattern matches valid PostgreSQL schema names (max 63 chars).
var schemaNamePattern = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]{0,62}$`)

// ValidateSchemaName validates that a schema name is a safe identifier.
func ValidateSchemaName(name string) error {
	if name == "" {
		return fmt.Errorf("schema name is required")
	}
	if !schemaNamePattern.MatchString(name) {
		return fmt.Errorf("invalid schema name: %q", name)
	}
	return nil
}

// numericPattern matches integer and decimal literals (optionally negative).
var numericPattern = regexp.MustCompile(`^-?\d+(\.\d+)?$`)

// nextvalPattern matches nextval('sequence_name') with safe sequence names.
var nextvalPattern = regexp.MustCompile(`^nextval\('[a-zA-Z_][a-zA-Z0-9_.]*'\)$`)

// safeFunctions is the allowlist of PostgreSQL functions/keywords allowed in DEFAULT expressions.
var safeFunctions = map[string]bool{
	"now()":                   true,
	"gen_random_uuid()":       true,
	"uuid_generate_v4()":      true,
	"current_timestamp":       true,
	"current_date":            true,
	"current_user":            true,
	"clock_timestamp()":       true,
	"statement_timestamp()":   true,
	"transaction_timestamp()": true,
}

// ValidateDefaultExpression validates and sanitizes a column DEFAULT expression.
// It returns the safe SQL expression or an error if the input is rejected.
func ValidateDefaultExpression(expr string) (string, error) {
	if expr == "" {
		return "", fmt.Errorf("default expression is empty")
	}

	// Reject dangerous patterns
	if strings.Contains(expr, ";") || strings.Contains(expr, "--") || strings.Contains(expr, "/*") {
		return "", fmt.Errorf("default expression contains forbidden characters: %q", expr)
	}

	// Numeric literals
	if numericPattern.MatchString(expr) {
		return expr, nil
	}

	// Boolean/null literals
	switch strings.ToLower(expr) {
	case "true", "false", "null":
		return expr, nil
	}

	// Safe function calls (case-insensitive lookup)
	if safeFunctions[strings.ToLower(expr)] {
		return expr, nil
	}

	// nextval('sequence_name') pattern
	if nextvalPattern.MatchString(expr) {
		return expr, nil
	}

	// If it looks like a plain string (no parens, no operators), wrap as literal
	if !strings.ContainsAny(expr, "()=<>+*/%|&^~!@#${}[]") {
		return QuoteLiteral(expr), nil
	}

	return "", fmt.Errorf("unrecognized default expression: %q", expr)
}

// MaxPolicyExpressionLen caps the length of a USING / WITH CHECK clause to
// stop pathologically large inputs. RLS expressions in practice are short
// boolean predicates over the row's columns and a few session GUCs.
const MaxPolicyExpressionLen = 1024

// rejectedPolicyKeywords are SQL keywords that have no place in an RLS
// boolean predicate. Their presence almost always indicates injection
// (statement chaining or DDL/DML smuggled into the expression).
var rejectedPolicyKeywords = []string{
	"insert", "update", "delete", "drop", "alter", "create", "grant",
	"revoke", "truncate", "merge", "copy", "execute", "do",
	"pg_sleep", "pg_read_file", "pg_ls_dir", "lo_import", "lo_export",
}

// ValidatePolicyExpression performs a defense-in-depth check on RLS
// USING / WITH CHECK expressions before they are interpolated into a
// CREATE POLICY DDL statement. PostgreSQL has no parameterised form for
// policy bodies (DDL is not parameterised), so the only mitigation is
// strict input shaping. The caller must already be authorised to manage
// schema for the project — this is a second layer in case that check is
// bypassed or the role's DB privileges expand later.
//
// Rejects:
//   - statement terminators (";"), comments ("--", "/*", "*/")
//   - dollar-quoted strings ("$$" or "$tag$") — these escape every other guard
//   - DDL/DML keywords (INSERT/UPDATE/DELETE/DROP/...)
//   - empty input or input over MaxPolicyExpressionLen
//
// Accepts: anything else verbatim. RLS predicates can legitimately contain
// arbitrary operators, function calls, identifiers, and parentheses, so
// this remains a deny-list rather than an allow-list. Operators with
// schema-write power (platform_admin / org admin) should be the only ones
// reaching this path.
func ValidatePolicyExpression(expr string) (string, error) {
	expr = strings.TrimSpace(expr)
	if expr == "" {
		return "", fmt.Errorf("policy expression is empty")
	}
	if len(expr) > MaxPolicyExpressionLen {
		return "", fmt.Errorf("policy expression exceeds %d chars", MaxPolicyExpressionLen)
	}
	if err := checkForbiddenTokens(expr); err != nil {
		return "", err
	}
	lower := strings.ToLower(expr)
	for _, kw := range rejectedPolicyKeywords {
		if err := checkKeywordBoundary(lower, kw); err != nil {
			return "", err
		}
	}
	return expr, nil
}

// checkForbiddenTokens rejects SQL injection tokens: statement terminators,
// comments, and dollar-quoted strings.
func checkForbiddenTokens(expr string) error {
	if strings.ContainsAny(expr, ";") || strings.Contains(expr, "--") ||
		strings.Contains(expr, "/*") || strings.Contains(expr, "*/") ||
		strings.Contains(expr, "$$") {
		return fmt.Errorf("policy expression contains forbidden token")
	}
	if strings.Count(expr, "$") >= 2 {
		return fmt.Errorf("policy expression contains $-tagged token")
	}
	return nil
}

// checkKeywordBoundary returns an error if kw appears as a whole word in lower.
// Word-boundary check prevents "user_create_at" from matching "create".
func checkKeywordBoundary(lower, kw string) error {
	idx := 0
	for {
		pos := strings.Index(lower[idx:], kw)
		if pos < 0 {
			return nil
		}
		at := idx + pos
		before := byte(' ')
		after := byte(' ')
		if at > 0 {
			before = lower[at-1]
		}
		if at+len(kw) < len(lower) {
			after = lower[at+len(kw)]
		}
		if !isIdentChar(before) && !isIdentChar(after) {
			return fmt.Errorf("policy expression contains forbidden keyword %q", kw)
		}
		idx = at + len(kw)
	}
}

func isIdentChar(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') ||
		(b >= '0' && b <= '9') || b == '_'
}

// QuoteRoles quotes a comma-separated roles string. Each role name gets quoted.
func QuoteRoles(roles string) string {
	parts := strings.Split(roles, ",")
	quoted := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		// "public" is a special keyword in PostgreSQL, not an identifier
		if strings.ToLower(p) == "public" {
			quoted = append(quoted, "public")
		} else {
			quoted = append(quoted, QuoteIdent(p))
		}
	}
	return strings.Join(quoted, ", ")
}
