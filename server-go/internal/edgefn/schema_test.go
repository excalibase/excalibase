package edgefn

import (
	"encoding/json"
	"strings"
	"testing"
)

// schemaTestBundle is the inline `@excalibase/server` shim the schema-extract
// tests bundle alongside the user's schema.ts. It must mirror the real lib's
// side-channel (writing globalThis.__excalibase_schema). The shim covers just
// enough of the API surface that defineSchema runs to completion under
// esbuild's IIFE wrapper.
const schemaTestBundle = `
const validator = (kind, extra = {}) => ({
  kind,
  parse: (v) => v,
  toJsonSchema: () => ({ type: kind, ...extra }),
  ...extra,
});
export const v = {
  string: () => validator("string"),
  number: () => validator("number"),
  boolean: () => validator("boolean"),
  id: (table) => ({
    kind: "id",
    tableName: table,
    parse: (v) => v,
    toJsonSchema: () => ({ type: "string", "x-convex-id": table }),
  }),
  array: (inner) => ({
    kind: "array",
    parse: (v) => v,
    toJsonSchema: () => ({ type: "array", items: inner.toJsonSchema() }),
  }),
  object: (shape) => ({
    kind: "object",
    shape,
    parse: (v) => v,
    toJsonSchema: () => {
      const properties = {};
      const required = [];
      for (const k of Object.keys(shape)) {
        properties[k] = shape[k].toJsonSchema();
        if (shape[k].kind !== "optional") required.push(k);
      }
      return { type: "object", properties, required, additionalProperties: false };
    },
  }),
  optional: (inner) => ({
    kind: "optional",
    inner,
    parse: (v) => v,
    toJsonSchema: () => inner.toJsonSchema(),
  }),
};
export function defineTable(validator) {
  const state = { validator, indexes: [], searchIndexes: [], vectorIndexes: [] };
  function rebuild() {
    return {
      ...state,
      index(name, fields) {
        state.indexes.push({ name, fields });
        return rebuild();
      },
      searchIndex(name, opts) {
        state.searchIndexes.push({
          name,
          searchField: opts.searchField,
          filterFields: opts.filterFields || [],
        });
        return rebuild();
      },
      vectorIndex(name, opts) {
        state.vectorIndexes.push({
          name,
          vectorField: opts.vectorField,
          dimensions: opts.dimensions,
          filterFields: opts.filterFields || [],
        });
        return rebuild();
      },
    };
  }
  return rebuild();
}
export function defineSchema(tables) {
  const out = { tables: {} };
  for (const [name, builder] of Object.entries(tables)) {
    const base = builder.validator.toJsonSchema();
    const properties = { ...base.properties };
    properties._id = { type: "string", "x-convex-id": name };
    properties._creationTime = { type: "number" };
    out.tables[name] = {
      validator: { type: "object", properties, required: base.required, additionalProperties: false },
      indexes: builder.indexes,
      searchIndexes: builder.searchIndexes,
      vectorIndexes: builder.vectorIndexes,
    };
  }
  globalThis.__excalibase_schema = out;
  return { tables, toJsonSchema: () => out };
}
`

func TestBundle_ExtractsSchemaJSON(t *testing.T) {
	userSchema := `
import { defineSchema, defineTable, v } from "./_lib.ts";
export default defineSchema({
  users: defineTable(v.object({ name: v.string(), age: v.number() })).index("by_name", ["name"]),
  posts: defineTable(v.object({ title: v.string(), authorId: v.id("users") })),
});
`
	fn := &Function{
		ProjectID: "proj_test0001",
		ID:        "schema-app",
		Name:      "Schema App",
		Files: []File{
			{Path: "_lib.ts", Content: schemaTestBundle},
			{Path: "index.ts", Content: userSchema},
		},
	}
	code, err := fn.Bundle()
	if err != nil {
		t.Fatalf("Bundle: %v", err)
	}
	schema, found, err := ExtractSchema(code)
	if err != nil {
		t.Fatalf("ExtractSchema: %v", err)
	}
	if !found {
		t.Fatal("expected schema to be extracted, got found=false")
	}
	if len(schema.Tables) != 2 {
		t.Fatalf("expected 2 tables, got %d", len(schema.Tables))
	}
	users, ok := schema.Tables["users"]
	if !ok {
		t.Fatal("expected users table in extracted schema")
	}
	// _id and _creationTime should be auto-injected.
	props := users.Validator.Properties
	if _, ok := props["_id"]; !ok {
		t.Error("users table missing _id system field")
	}
	if _, ok := props["_creationTime"]; !ok {
		t.Error("users table missing _creationTime system field")
	}
	if len(users.Indexes) != 1 || users.Indexes[0].Name != "by_name" {
		t.Errorf("users index by_name missing, got: %+v", users.Indexes)
	}
}

func TestBundle_NoSchema_ReturnsFoundFalse(t *testing.T) {
	fn := &Function{
		ProjectID: "proj_test0001",
		ID:        "no-schema",
		Name:      "No Schema",
		Files: []File{
			{Path: testIndexTS, Content: `
export default {
  kind: "query",
  args: { parse: (a) => a },
  handler: async (ctx, args) => ({ ok: true }),
}`},
		},
	}
	code, err := fn.Bundle()
	if err != nil {
		t.Fatalf("Bundle: %v", err)
	}
	_, found, err := ExtractSchema(code)
	if err != nil {
		t.Fatalf("ExtractSchema: %v", err)
	}
	if found {
		t.Error("expected found=false on bundle without defineSchema, got true")
	}
}

func TestExtractSchema_FunctionGetsSchemaJSONField(t *testing.T) {
	userSchema := `
import { defineSchema, defineTable, v } from "./_lib.ts";
export default defineSchema({
  items: defineTable(v.object({ name: v.string() })),
});
`
	fn := &Function{
		ProjectID: "proj_test0001",
		ID:        "with-schema",
		Name:      "WithSchema",
		Files: []File{
			{Path: "_lib.ts", Content: schemaTestBundle},
			{Path: "index.ts", Content: userSchema},
		},
	}
	code, err := fn.Bundle()
	if err != nil {
		t.Fatalf("Bundle: %v", err)
	}
	schema, found, err := ExtractSchema(code)
	if err != nil {
		t.Fatalf("ExtractSchema: %v", err)
	}
	if !found {
		t.Fatal("expected schema found")
	}
	raw, err := json.Marshal(schema)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	// Confirm the JSON is round-trippable and contains the table.
	if !strings.Contains(string(raw), `"items"`) {
		t.Errorf("expected items table in serialized schema, got: %s", string(raw))
	}
	fn.SchemaJSON = raw
	if len(fn.SchemaJSON) == 0 {
		t.Error("SchemaJSON should be populated on Function")
	}
}
