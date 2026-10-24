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

func TestQuoteIdentEscapesEmbeddedQuotes(t *testing.T) {
	if got := quoteIdent(`a"; DROP TABLE x; --`); got != `"a""; DROP TABLE x; --"` {
		t.Errorf("quoteIdent: got %q", got)
	}
}

func TestMigrationDDLQuotesEveryNameAndPath(t *testing.T) {
	if got, want := createNosqlSchemaSQL, "CREATE SCHEMA IF NOT EXISTS "+quoteIdent(nosqlSchemaName); got != want {
		t.Errorf("create schema: got %q want %q", got, want)
	}
	if got := buildNosqlCreateTableSQL("users"); !strings.HasPrefix(got, `CREATE TABLE IF NOT EXISTS "nosql"."users" (`) {
		t.Errorf("create table: got %q", got)
	}
	if got, want := buildValidatorCommentSQL("users", `{"a":"it's"}`), `COMMENT ON TABLE "nosql"."users" IS '{"a":"it''s"}'`; got != want {
		t.Errorf("comment: got %q want %q", got, want)
	}

	steps, err := buildBtreeIndexSQL("users", IndexSpec{Name: "by_email", Fields: []string{"email", "org"}})
	if err != nil {
		t.Fatal(err)
	}
	if want := `CREATE INDEX IF NOT EXISTS "users_by_email" ON "nosql"."users" ((doc ->> 'email'), (doc ->> 'org'))`; steps.sql != want {
		t.Errorf("btree: got %q want %q", steps.sql, want)
	}

	search, err := buildSearchIndexSQL("users", SearchIndexSpec{Name: "s", SearchField: "bio"})
	if err != nil {
		t.Fatal(err)
	}
	wantSearch := []string{
		`ALTER TABLE "nosql"."users" ADD COLUMN IF NOT EXISTS "search_vector_bio" tsvector GENERATED ALWAYS AS (to_tsvector('simple', coalesce(doc ->> 'bio', ''))) STORED`,
		`CREATE INDEX IF NOT EXISTS "users_search_s" ON "nosql"."users" USING GIN ("search_vector_bio")`,
	}
	for i, step := range search {
		if step.sql != wantSearch[i] {
			t.Errorf("search %d: got %q want %q", i, step.sql, wantSearch[i])
		}
	}

	vector, err := buildVectorIndexSQL("users", VectorIndexSpec{Name: "v", VectorField: "emb", Dimensions: 3})
	if err != nil {
		t.Fatal(err)
	}
	wantVector := []string{
		`ALTER TABLE "nosql"."users" ADD COLUMN IF NOT EXISTS "embedding_emb" vector(3)`,
		`CREATE INDEX IF NOT EXISTS "users_vector_v" ON "nosql"."users" USING ivfflat ("embedding_emb" vector_cosine_ops)`,
	}
	for i, step := range vector {
		if step.sql != wantVector[i] {
			t.Errorf("vector %d: got %q want %q", i, step.sql, wantVector[i])
		}
	}
}

func TestMigrationDDLRefusesAnInjectedField(t *testing.T) {
	evil := "x') OR 1=1; DROP TABLE users; --"
	if _, err := buildBtreeIndexSQL("users", IndexSpec{Name: "i", Fields: []string{evil}}); err == nil {
		t.Error("btree index accepted an injected field")
	}
	if _, err := buildSearchIndexSQL("users", SearchIndexSpec{Name: "s", SearchField: evil}); err == nil {
		t.Error("search index accepted an injected field")
	}
	if _, err := buildVectorIndexSQL("users", VectorIndexSpec{Name: "v", VectorField: evil, Dimensions: 3}); err == nil {
		t.Error("vector index accepted an injected field")
	}
}
