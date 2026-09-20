package edgefn

import (
	"strings"
	"testing"
)

func TestValidateIdent_RejectsBadNames(t *testing.T) {
	bad := []string{
		"", "Hello", "0starts_with_digit", "has space", "drop-table", "ñ",
		strings.Repeat("a", 64),
	}
	for _, ident := range bad {
		if err := validateIdent("test", ident); err == nil {
			t.Errorf("validateIdent(%q): expected error, got nil", ident)
		}
	}
}

func TestValidateIdent_AcceptsSafeNames(t *testing.T) {
	good := []string{"users", "users_table", "_private", "u1", "a_b_c"}
	for _, ident := range good {
		if err := validateIdent("test", ident); err != nil {
			t.Errorf("validateIdent(%q): unexpected error: %v", ident, err)
		}
	}
}

func TestQuoteIdent(t *testing.T) {
	if got := quoteIdent("foo"); got != `"foo"` {
		t.Errorf("quoteIdent: got %q want %q", got, `"foo"`)
	}
}

func TestQuoteLiteral(t *testing.T) {
	cases := map[string]string{
		"hello":    `'hello'`,
		"o'reilly": `'o''reilly'`,
		"":         `''`,
		"a'b'c":    `'a''b''c'`,
	}
	for in, want := range cases {
		if got := quoteLiteral(in); got != want {
			t.Errorf("quoteLiteral(%q): got %q want %q", in, got, want)
		}
	}
}

// TestExtractSchema_BadJSEvalReturnsError exercises the JS-eval failure
// branch — ensures the bundle that mentions defineSchema but is syntactically
// broken surfaces as a hard error to the deploy flow.
func TestExtractSchema_BadJSEvalReturnsError(t *testing.T) {
	bundle := "defineSchema( <<<syntax error>>>"
	if _, _, err := ExtractSchema(bundle); err == nil {
		t.Error("expected error on malformed bundle, got nil")
	}
}

// TestExtractSchema_NoMatchSkipsEval — the fast-path that avoids spinning up
// goja when defineSchema is not referenced at all.
func TestExtractSchema_NoMatchSkipsEval(t *testing.T) {
	bundle := "console.log('no schema here');"
	schema, found, err := ExtractSchema(bundle)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if found {
		t.Error("expected found=false")
	}
	if len(schema.Tables) != 0 {
		t.Errorf("expected empty schema, got: %+v", schema)
	}
}

// TestExtractSchema_NullSlotReturnsFoundFalse — defineSchema appears in the
// bundle but the slot stays null (e.g. inside a dead branch).
func TestExtractSchema_NullSlotReturnsFoundFalse(t *testing.T) {
	bundle := `
		if (false) {
			defineSchema({});
		}
		globalThis.__excalibase_schema = null;
	`
	schema, found, err := ExtractSchema(bundle)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if found {
		t.Errorf("expected found=false, schema=%+v", schema)
	}
}

// TestApplySchema_EmptyTablesIsNoOp — short-circuit when no tables declared.
func TestApplySchema_EmptyTablesIsNoOp(t *testing.T) {
	// Pass nil DB — should never be dereferenced because we return early.
	if err := ApplySchema(nil, nil, "proj_test", Schema{}); err != nil {
		t.Errorf("empty schema: expected nil error, got: %v", err)
	}
}

// TestExtractSchema_DefineSchemaReferencedButThrows — bundle calls
// defineSchema but the user's runtime throws; we surface the error so the
// deploy fails atomically.
func TestExtractSchema_DefineSchemaReferencedButThrows(t *testing.T) {
	bundle := `defineSchema(); throw new Error("kaboom");`
	if _, _, err := ExtractSchema(bundle); err == nil {
		t.Error("expected runtime error to surface")
	}
}

// TestExtractSchema_DefineSchemaButNoLib — defineSchema is referenced as a
// bare identifier but never assigned globally and never called as a
// function, so the slot stays at the init value.
func TestExtractSchema_StringMentionOnlyTriggersEval(t *testing.T) {
	// String containing the literal "defineSchema(" — the heuristic does
	// NOT scan tokens, only the raw text. The eval still runs, the slot
	// stays null, and we report found=false cleanly.
	bundle := `
		const note = "defineSchema(...) is great";
		// no actual schema definition; the slot must be null
	`
	schema, found, err := ExtractSchema(bundle)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if found {
		t.Errorf("expected found=false, got: %+v", schema)
	}
}
