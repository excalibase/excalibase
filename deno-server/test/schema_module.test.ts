// Unit tests for `runtime/schema.ts` — the worker-side helper that reads
// the bundler-emitted `__excalibase_function_metadata.schemaJson` and
// exposes a small lookup surface to the rest of the runtime.

import { assertEquals } from "https://deno.land/std@0.224.0/assert/mod.ts";
import { getSchema, getTableDef } from "../runtime/schema.ts";

interface MutableGlobal {
  __excalibase_function_metadata?: { schemaJson?: unknown };
}

function setMeta(payload: unknown): void {
  (globalThis as unknown as MutableGlobal).__excalibase_function_metadata = {
    schemaJson: payload,
  };
}

function clearMeta(): void {
  (globalThis as unknown as MutableGlobal).__excalibase_function_metadata = undefined;
}

Deno.test("getSchema returns null when no metadata is set", () => {
  clearMeta();
  assertEquals(getSchema(), null);
});

Deno.test("getSchema parses the bundler's schemaJson side-channel", () => {
  setMeta({
    tables: {
      users: {
        validator: { type: "object", properties: {}, required: [], additionalProperties: false },
        indexes: [{ name: "by_email", fields: ["email"] }],
        searchIndexes: [],
        vectorIndexes: [],
      },
    },
  });
  const schema = getSchema();
  if (!schema) throw new Error("expected non-null schema");
  assertEquals(Object.keys(schema.tables), ["users"]);
});

Deno.test("getTableDef returns the declared table, undefined for absent", () => {
  setMeta({
    tables: {
      products: {
        validator: { type: "object", properties: {}, required: [], additionalProperties: false },
        indexes: [],
        searchIndexes: [{ name: "by_name", searchField: "name", filterFields: [] }],
        vectorIndexes: [],
      },
    },
  });
  const def = getTableDef("products");
  if (!def) throw new Error("expected products table def");
  assertEquals(def.searchIndexes.length, 1);
  assertEquals(getTableDef("missing"), undefined);
});

Deno.test("getTableDef.indexes is the right shape for the Phase 6 query builder", () => {
  // Phase 6's .withIndex("by_x") call will look up the index by name on
  // getTableDef(name).indexes; we lock the shape in here so a Phase 6 PR
  // doesn't have to also rewrite the metadata pipe.
  setMeta({
    tables: {
      orders: {
        validator: { type: "object", properties: {}, required: [], additionalProperties: false },
        indexes: [{ name: "by_status", fields: ["status"] }],
        searchIndexes: [],
        vectorIndexes: [],
      },
    },
  });
  const def = getTableDef("orders");
  if (!def) throw new Error("expected orders table def");
  assertEquals(def.indexes[0].name, "by_status");
  assertEquals(def.indexes[0].fields, ["status"]);
});
