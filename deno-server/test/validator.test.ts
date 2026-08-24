// validator.ts tests — cover both the no-schema path (collection_schema
// table absent) and the schema-present path (validation fires + returns
// issues). The schema source is the optional nosql.collection_schema table
// per spec; when it doesn't exist the validator is a no-op.

import { assertEquals } from "https://deno.land/std@0.224.0/assert/mod.ts";
import { newCache, validateDoc } from "../runtime/validator.ts";
import { createCollection, startPostgres } from "./pg_harness.ts";

async function bootSql(pgUrl: string) {
  const postgres = (await import("npm:postgres@3.4.4")).default;
  // deno-lint-ignore no-explicit-any
  return postgres(pgUrl, { onnotice: () => {} }) as any;
}

Deno.test({
  name: "validateDoc is no-op when collection_schema table is missing",
  async fn() {
    const pg = await startPostgres();
    try {
      await createCollection(pg.url, "u");
      const sql = await bootSql(pg.url);
      try {
        const issues = await validateDoc(sql, newCache(), "u", { name: "x" });
        assertEquals(issues, []);
      } finally {
        await sql.end({ timeout: 1 });
      }
    } finally {
      await pg.stop();
    }
  },
  sanitizeOps: false,
  sanitizeResources: false,
});

Deno.test({
  name: "validateDoc enforces a registered schema and reports issues",
  async fn() {
    const pg = await startPostgres();
    try {
      await createCollection(pg.url, "u");
      const sql = await bootSql(pg.url);
      try {
        // Create the schema-registry table and register a JSON Schema that
        // requires `name` (string) and `age` (integer >= 0).
        await sql`CREATE TABLE IF NOT EXISTS nosql.collection_schema (
          collection text PRIMARY KEY,
          schema jsonb NOT NULL
        )`;
        const schema = {
          type: "object",
          properties: {
            name: { type: "string" },
            age: { type: "integer", minimum: 0 },
          },
          required: ["name", "age"],
          additionalProperties: true,
        };
        await sql`INSERT INTO nosql.collection_schema (collection, schema)
                  VALUES ('u', ${sql.json(schema)})
                  ON CONFLICT (collection) DO UPDATE SET schema = EXCLUDED.schema`;

        // Valid doc → no issues.
        const ok = await validateDoc(sql, newCache(), "u", { name: "alice", age: 30 });
        assertEquals(ok, []);

        // Missing required → issue.
        const bad = await validateDoc(sql, newCache(), "u", { name: "alice" });
        assertEquals(bad.length > 0, true);
        assertEquals(bad.some((i) => i.message.toLowerCase().includes("required")), true);
      } finally {
        await sql.end({ timeout: 1 });
      }
    } finally {
      await pg.stop();
    }
  },
  sanitizeOps: false,
  sanitizeResources: false,
});

Deno.test({
  name: "validateDoc reuses cache across calls for the same collection",
  async fn() {
    const pg = await startPostgres();
    try {
      await createCollection(pg.url, "u");
      const sql = await bootSql(pg.url);
      try {
        const cache = newCache();
        // First call populates the cache (with null since the table is
        // absent), subsequent calls must short-circuit.
        const a = await validateDoc(sql, cache, "u", { name: "x" });
        const b = await validateDoc(sql, cache, "u", { name: "y" });
        assertEquals(a, []);
        assertEquals(b, []);
        assertEquals(cache.compiled.has("u"), true);
      } finally {
        await sql.end({ timeout: 1 });
      }
    } finally {
      await pg.stop();
    }
  },
  sanitizeOps: false,
  sanitizeResources: false,
});
