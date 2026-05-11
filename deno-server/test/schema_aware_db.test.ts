// Schema-aware preflight tests for ctx.db.
//
// Phase 5b wires the bundler-emitted schema (Phase 5a's
// __excalibase_function_metadata.schemaJson side-channel) into the worker
// so the runtime can fail-fast with a friendly error when:
//   * a collection name not declared in schema.ts is addressed via
//     `ctx.db.collection(name)`, or
//   * `.search(...)` is called on a collection that has no searchIndex
//     declared, or
//   * `.vectorSearch(...)` is called on a collection that has no
//     vectorIndex declared.
//
// In schema-less bundles (no schema.ts emitted), the runtime falls back to
// the permissive Phase 1.5 behaviour: any collection name is accepted, and
// missing search/vector columns surface as the underlying Postgres
// SQLSTATE 42703 ("undefined column").

import { assertEquals } from "https://deno.land/std@0.224.0/assert/mod.ts";
import { startRuntime } from "./harness.ts";
import { createCollection, startPostgres } from "./pg_harness.ts";

// A bundle preamble that injects `__excalibase_function_metadata.schemaJson`
// the same way the Go bundler does after Phase 5a. The shape mirrors the
// SchemaJsonSchema export from @excalibase/server: `{ tables: { name: {
// validator, indexes, searchIndexes, vectorIndexes } } }`. We keep the
// validator skeletal — the runtime preflight only consults the index
// arrays plus the set of declared table names.
function withSchemaBundle(schemaJson: unknown, expr: string): string {
  return `
    globalThis.__excalibase_function_metadata = { schemaJson: ${JSON.stringify(schemaJson)} };
    globalThis.__excalibase_default = ${expr};
  `;
}

Deno.test({
  name: "schema-aware: unknown collection throws a friendly error",
  async fn() {
    const pg = await startPostgres();
    try {
      await createCollection(pg.url, "users");
      const rt = await startRuntime({
        v2Enabled: true,
        allowedHosts: `${pg.host}:${pg.port}`,
        dbUrl: pg.url,
      });
      try {
        const schema = {
          tables: {
            users: {
              validator: { type: "object", properties: {}, required: [], additionalProperties: false },
              indexes: [],
              searchIndexes: [],
              vectorIndexes: [],
            },
          },
        };
        const code = withSchemaBundle(schema, `{
          kind: "mutation",
          args: { parse: (a) => a },
          handler: async (ctx, _args) => {
            try {
              await ctx.db.collection("not_in_schema").insert({ x: 1 });
              return { ok: true };
            } catch (e) {
              return { error: String(e && e.message || e) };
            }
          },
        }`);
        await rt.deploy("schemaful-unknown", code);
        const res = await rt.invoke("schemaful-unknown", { args: {} });
        const parsed = JSON.parse(res.body);
        if (parsed.data?.ok === true) {
          throw new Error("expected unknown collection to throw");
        }
        assertEquals(typeof parsed.data.error, "string");
        if (!parsed.data.error.includes("not_in_schema") || !parsed.data.error.includes("schema")) {
          throw new Error(`error must reference the collection name and the schema: ${parsed.data.error}`);
        }
      } finally {
        await rt.stop();
      }
    } finally {
      await pg.stop();
    }
  },
  sanitizeOps: false,
  sanitizeResources: false,
});

Deno.test({
  name: "schema-aware: search on a collection without searchIndex throws preflight error",
  async fn() {
    const pg = await startPostgres();
    try {
      await createCollection(pg.url, "users");
      const rt = await startRuntime({
        v2Enabled: true,
        allowedHosts: `${pg.host}:${pg.port}`,
        dbUrl: pg.url,
      });
      try {
        const schema = {
          tables: {
            users: {
              validator: { type: "object", properties: {}, required: [], additionalProperties: false },
              indexes: [],
              searchIndexes: [],
              vectorIndexes: [],
            },
          },
        };
        const code = withSchemaBundle(schema, `{
          kind: "query",
          args: { parse: (a) => a },
          handler: async (ctx, _args) => {
            try {
              await ctx.db.collection("users").search("anything");
              return { ok: true };
            } catch (e) {
              return { error: String(e && e.message || e) };
            }
          },
        }`);
        await rt.deploy("schemaful-no-search", code);
        const res = await rt.invoke("schemaful-no-search", { args: {} });
        const parsed = JSON.parse(res.body);
        if (parsed.data?.ok === true) {
          throw new Error("expected search without searchIndex to throw");
        }
        if (!parsed.data.error.toLowerCase().includes("search")) {
          throw new Error(`error must mention search: ${parsed.data.error}`);
        }
      } finally {
        await rt.stop();
      }
    } finally {
      await pg.stop();
    }
  },
  sanitizeOps: false,
  sanitizeResources: false,
});

Deno.test({
  name: "schema-aware: vectorSearch on a collection without vectorIndex throws preflight error",
  async fn() {
    const pg = await startPostgres();
    try {
      await createCollection(pg.url, "users");
      const rt = await startRuntime({
        v2Enabled: true,
        allowedHosts: `${pg.host}:${pg.port}`,
        dbUrl: pg.url,
      });
      try {
        const schema = {
          tables: {
            users: {
              validator: { type: "object", properties: {}, required: [], additionalProperties: false },
              indexes: [],
              searchIndexes: [],
              vectorIndexes: [],
            },
          },
        };
        const code = withSchemaBundle(schema, `{
          kind: "query",
          args: { parse: (a) => a },
          handler: async (ctx, _args) => {
            try {
              await ctx.db.collection("users").vectorSearch([0.1, 0.2]);
              return { ok: true };
            } catch (e) {
              return { error: String(e && e.message || e) };
            }
          },
        }`);
        await rt.deploy("schemaful-no-vec", code);
        const res = await rt.invoke("schemaful-no-vec", { args: {} });
        const parsed = JSON.parse(res.body);
        if (parsed.data?.ok === true) {
          throw new Error("expected vectorSearch without vectorIndex to throw");
        }
        if (!parsed.data.error.toLowerCase().includes("vector")) {
          throw new Error(`error must mention vector: ${parsed.data.error}`);
        }
      } finally {
        await rt.stop();
      }
    } finally {
      await pg.stop();
    }
  },
  sanitizeOps: false,
  sanitizeResources: false,
});

Deno.test({
  name: "schema-less: permissive mode — any collection name OK, no preflight",
  async fn() {
    const pg = await startPostgres();
    try {
      await createCollection(pg.url, "permissive");
      const rt = await startRuntime({
        v2Enabled: true,
        allowedHosts: `${pg.host}:${pg.port}`,
        dbUrl: pg.url,
      });
      try {
        // No __excalibase_function_metadata is set — bundler did not emit a
        // schema. Runtime must accept any collection name; only the
        // Postgres layer enforces that the table exists.
        const code = `globalThis.__excalibase_default = {
          kind: "mutation",
          args: { parse: (a) => a },
          handler: async (ctx, _args) => {
            const inserted = await ctx.db.collection("permissive").insert({ foo: "bar" });
            return inserted;
          },
        };`;
        await rt.deploy("schemaless-ok", code);
        const res = await rt.invoke("schemaless-ok", { args: {} });
        const parsed = JSON.parse(res.body);
        assertEquals(typeof parsed.data._id, "string");
        assertEquals(typeof parsed.data._creationTime, "number");
        assertEquals(parsed.data.foo, "bar");
      } finally {
        await rt.stop();
      }
    } finally {
      await pg.stop();
    }
  },
  sanitizeOps: false,
  sanitizeResources: false,
});
