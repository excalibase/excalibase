package schema

import (
	"strings"
	"testing"
)

const (
	testGenUUID   = "gen_random_uuid()"
	testUUIDGen   = "uuid_generate_v4()"
	testClockTS   = "clock_timestamp()"
	testStmtTS    = "statement_timestamp()"
	testTxnTS     = "transaction_timestamp()"
)


func TestQuoteIdent(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{"simple", "users", `"users"`},
		{"with space", "my table", `"my table"`},
		{"with double quote", `my"table`, `"my""table"`},
		{"reserved word", "select", `"select"`},
		{"empty", "", `""`},
		{"schema qualified not handled", "public.users", `"public.users"`},
		{"with unicode", "tëst", `"tëst"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := QuoteIdent(tt.input)
			if got != tt.expected {
				t.Errorf("QuoteIdent(%q) = %q, want %q", tt.input, got, tt.expected)
			}
		})
	}
}

func TestValidatePolicyCommand(t *testing.T) {
	for _, valid := range []string{"ALL", "SELECT", "INSERT", "UPDATE", "DELETE", "all", "select"} {
		if err := ValidatePolicyCommand(valid); err != nil {
			t.Errorf("ValidatePolicyCommand(%q) should pass, got: %v", valid, err)
		}
	}
	for _, invalid := range []string{"ALL; DROP TABLE users--", "TRUNCATE", "", "SEL ECT"} {
		if err := ValidatePolicyCommand(invalid); err == nil {
			t.Errorf("ValidatePolicyCommand(%q) should fail", invalid)
		}
	}
}

func TestValidateLanguage(t *testing.T) {
	for _, valid := range []string{"sql", "plpgsql", "SQL", "PLPGSQL"} {
		if err := ValidateLanguage(valid); err != nil {
			t.Errorf("ValidateLanguage(%q) should pass, got: %v", valid, err)
		}
	}
	for _, invalid := range []string{"sql; DROP TABLE--", "javascript", ""} {
		if err := ValidateLanguage(invalid); err == nil {
			t.Errorf("ValidateLanguage(%q) should fail", invalid)
		}
	}
}

func TestValidateVolatility(t *testing.T) {
	for _, valid := range []string{"VOLATILE", "STABLE", "IMMUTABLE", "volatile"} {
		if err := ValidateVolatility(valid); err != nil {
			t.Errorf("ValidateVolatility(%q) should pass, got: %v", valid, err)
		}
	}
	for _, invalid := range []string{"VOLATILE; DROP TABLE--", "FAST", ""} {
		if err := ValidateVolatility(invalid); err == nil {
			t.Errorf("ValidateVolatility(%q) should fail", invalid)
		}
	}
}

func TestValidateTypeName(t *testing.T) {
	for _, valid := range []string{"integer", "varchar(255)", "character varying", "int[]", "numeric(10,2)"} {
		if err := ValidateTypeName(valid); err != nil {
			t.Errorf("ValidateTypeName(%q) should pass, got: %v", valid, err)
		}
	}
	for _, invalid := range []string{"integer; DROP TABLE users--", "text'", `text"`, "", "int;bad"} {
		if err := ValidateTypeName(invalid); err == nil {
			t.Errorf("ValidateTypeName(%q) should fail", invalid)
		}
	}
}

func TestValidateArgTypes(t *testing.T) {
	for _, valid := range []string{"", "integer", "text, integer", "character varying"} {
		if err := ValidateArgTypes(valid); err != nil {
			t.Errorf("ValidateArgTypes(%q) should pass, got: %v", valid, err)
		}
	}
	for _, invalid := range []string{"); DROP TABLE users--", "text; bad", "int'bad"} {
		if err := ValidateArgTypes(invalid); err == nil {
			t.Errorf("ValidateArgTypes(%q) should fail", invalid)
		}
	}
}

func TestQuoteRoles(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"public", "public"},
		{"my_role", `"my_role"`},
		{"public, my_role", `public, "my_role"`},
		{"role1, role2", `"role1", "role2"`},
	}
	for _, tt := range tests {
		got := QuoteRoles(tt.input)
		if got != tt.expected {
			t.Errorf("QuoteRoles(%q) = %q, want %q", tt.input, got, tt.expected)
		}
	}
}

func TestValidateColumnName(t *testing.T) {
	for _, valid := range []string{"id", "user_name", "column1", "_private", "A"} {
		if err := ValidateColumnName(valid); err != nil {
			t.Errorf("ValidateColumnName(%q) should pass, got: %v", valid, err)
		}
	}
	for _, invalid := range []string{
		"", "1starts_with_digit", "has space", "has(paren)", "semi;colon",
		"quote'mark", `double"quote`, "col; DROP TABLE--", "a.b",
	} {
		if err := ValidateColumnName(invalid); err == nil {
			t.Errorf("ValidateColumnName(%q) should fail", invalid)
		}
	}
}

func TestValidateSchemaName(t *testing.T) {
	for _, valid := range []string{"public", "my_schema", "schema1", "_private"} {
		if err := ValidateSchemaName(valid); err != nil {
			t.Errorf("ValidateSchemaName(%q) should pass, got: %v", valid, err)
		}
	}
	for _, invalid := range []string{
		"", "1digit", "has space", "semi;colon", "a.b",
		"'; DROP TABLE--", strings.Repeat("a", 64),
	} {
		if err := ValidateSchemaName(invalid); err == nil {
			t.Errorf("ValidateSchemaName(%q) should fail", invalid)
		}
	}
}

func TestValidateDefaultExpression(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    string
		wantErr bool
	}{
		// Empty → error
		{"empty string", "", "", true},

		// Numeric literals
		{"integer", "42", "42", false},
		{"negative integer", "-7", "-7", false},
		{"decimal", "3.14", "3.14", false},
		{"negative decimal", "-0.5", "-0.5", false},

		// Boolean/null literals
		{"true lower", "true", "true", false},
		{"false lower", "false", "false", false},
		{"null lower", "null", "null", false},
		{"TRUE upper", "TRUE", "TRUE", false},
		{"FALSE upper", "FALSE", "FALSE", false},
		{"NULL upper", "NULL", "NULL", false},

		// String literals → wrapped with QuoteLiteral
		{"simple string", "hello", "'hello'", false},
		{"string with space", "hello world", "'hello world'", false},
		{"string with single quote", "it's", "'it''s'", false},

		// Safe function calls
		{"now()", "now()", "now()", false},
		{testGenUUID, testGenUUID, testGenUUID, false},
		{testUUIDGen, testUUIDGen, testUUIDGen, false},
		{"current_timestamp", "current_timestamp", "current_timestamp", false},
		{"CURRENT_TIMESTAMP", "CURRENT_TIMESTAMP", "CURRENT_TIMESTAMP", false},
		{"current_date", "current_date", "current_date", false},
		{"current_user", "current_user", "current_user", false},
		{testClockTS, testClockTS, testClockTS, false},
		{testStmtTS, testStmtTS, testStmtTS, false},
		{testTxnTS, testTxnTS, testTxnTS, false},

		// nextval pattern
		{"nextval simple", "nextval('my_seq')", "nextval('my_seq')", false},
		{"nextval schema qualified", "nextval('public.my_seq')", "nextval('public.my_seq')", false},

		// SQL injection attempts → rejected
		{"semicolon injection", "1; DROP TABLE users", "", true},
		{"comment dash injection", "1 -- DROP TABLE", "", true},
		{"comment block injection", "1 /* DROP */", "", true},
		{"nextval injection", "nextval('seq'); DROP TABLE users--')", "", true},

		// Unrecognized expressions → rejected
		{"subquery", "(SELECT 1)", "", true},
		{"function not in allowlist", "my_func()", "", true},
		{"cast expression", "CAST(1 AS text)", "", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ValidateDefaultExpression(tt.input)
			if tt.wantErr {
				if err == nil {
					t.Errorf("ValidateDefaultExpression(%q) expected error, got %q", tt.input, got)
				}
				return
			}
			if err != nil {
				t.Errorf("ValidateDefaultExpression(%q) unexpected error: %v", tt.input, err)
				return
			}
			if got != tt.want {
				t.Errorf("ValidateDefaultExpression(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestQuoteLiteral(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{"simple", "hello", `'hello'`},
		{"with single quote", "it's", `'it''s'`},
		{"with backslash", `a\b`, `'a\b'`},
		{"empty", "", `''`},
		{"with both quotes", `it's a "test"`, `'it''s a "test"'`},
		{"sql injection attempt", "'; DROP TABLE users; --", `'''; DROP TABLE users; --'`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := QuoteLiteral(tt.input)
			if got != tt.expected {
				t.Errorf("QuoteLiteral(%q) = %q, want %q", tt.input, got, tt.expected)
			}
		})
	}
}
