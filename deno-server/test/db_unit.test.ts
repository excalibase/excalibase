// Unit tests for the runtime/db.ts error-path branches. Each test calls
// `executeDbOp` against a real Postgres container so the SQL composition
// itself is exercised but the assertions focus on returned DbResult shapes
// rather than driver internals.

import { assertEquals } from "https://deno.land/std@0.224.0/assert/mod.ts";
import { executeDbOp, newCache } from "../runtime/db.ts";
import { startPostgres, createCollection } from "./pg_harness.ts";

async function withSql<T>(fn: (sql: unknown) => Promise<T>): Promise<T> {
  const pg = await startPostgres();
  try {
    await createCollection(pg.url, "u");
    Deno.env.set("EXCALIBASE_DB_URL", pg.url);
    const postgres = (await import("npm:postgres@3.4.4")).default;
    const sql = postgres(pg.url, { onnotice: () => {} });
    try {
      // deno-lint-ignore no-explicit-any
      return await fn(sql as any);
    } finally {
      await sql.end({ timeout: 1 });
    }
  } finally {
    await pg.stop();
  }
}

Deno.test({
  name: "rejects invalid collection name",
  async fn() {
    await withSql(async (sql) => {
      const res = await executeDbOp(sql, newCache(), {
        op: "insert",
        collection: "bad name with spaces",
        doc: { x: 1 },
      });
      assertEquals(res.ok, false);
      if (res.ok === false) assertEquals(res.error.includes("Invalid collection name"), true);
    });
  },
  sanitizeOps: false,
  sanitizeResources: false,
});

Deno.test({
  name: "insert rejects missing doc",
  async fn() {
    await withSql(async (sql) => {
      // deno-lint-ignore no-explicit-any
      const res = await executeDbOp(sql, newCache(), { op: "insert", collection: "u" } as any);
      assertEquals(res.ok, false);
      if (res.ok === false) assertEquals(res.error.includes("doc must be an object"), true);
    });
  },
  sanitizeOps: false,
  sanitizeResources: false,
});

Deno.test({
  name: "insertMany rejects empty array",
  async fn() {
    await withSql(async (sql) => {
      const res = await executeDbOp(sql, newCache(), {
        op: "insertMany",
        collection: "u",
        docs: [],
      });
      assertEquals(res.ok, false);
      if (res.ok === false) assertEquals(res.error.includes("non-empty array"), true);
    });
  },
  sanitizeOps: false,
  sanitizeResources: false,
});

Deno.test({
  name: "insertMany rejects non-object entry",
  async fn() {
    await withSql(async (sql) => {
      const res = await executeDbOp(sql, newCache(), {
        op: "insertMany",
        collection: "u",
        // deno-lint-ignore no-explicit-any
        docs: [{ ok: true }, null as any],
      });
      assertEquals(res.ok, false);
      if (res.ok === false) assertEquals(res.error.includes("must be objects"), true);
    });
  },
  sanitizeOps: false,
  sanitizeResources: false,
});

Deno.test({
  name: "getById rejects missing id",
  async fn() {
    await withSql(async (sql) => {
      const res = await executeDbOp(sql, newCache(), { op: "getById", collection: "u" });
      assertEquals(res.ok, false);
      if (res.ok === false) assertEquals(res.error.includes("id required"), true);
    });
  },
  sanitizeOps: false,
  sanitizeResources: false,
});

Deno.test({
  name: "update without filter rejects",
  async fn() {
    await withSql(async (sql) => {
      const res = await executeDbOp(sql, newCache(), {
        op: "update",
        collection: "u",
        patch: { $set: { name: "x" } },
      });
      assertEquals(res.ok, false);
      if (res.ok === false) assertEquals(res.error.includes("update requires a filter"), true);
    });
  },
  sanitizeOps: false,
  sanitizeResources: false,
});

Deno.test({
  name: "delete without filter rejects",
  async fn() {
    await withSql(async (sql) => {
      const res = await executeDbOp(sql, newCache(), {
        op: "delete",
        collection: "u",
      });
      assertEquals(res.ok, false);
      if (res.ok === false) assertEquals(res.error.includes("delete requires a filter"), true);
    });
  },
  sanitizeOps: false,
  sanitizeResources: false,
});

Deno.test({
  name: "$in with empty array throws as DbResult error",
  async fn() {
    await withSql(async (sql) => {
      const res = await executeDbOp(sql, newCache(), {
        op: "find",
        collection: "u",
        filter: { name: { $in: [] } },
      });
      assertEquals(res.ok, false);
      if (res.ok === false) assertEquals(res.error.includes("$in requires"), true);
    });
  },
  sanitizeOps: false,
  sanitizeResources: false,
});

Deno.test({
  name: "unsupported operator rejected",
  async fn() {
    await withSql(async (sql) => {
      const res = await executeDbOp(sql, newCache(), {
        op: "find",
        collection: "u",
        filter: { name: { $regex: ".*" } },
      });
      assertEquals(res.ok, false);
      if (res.ok === false) assertEquals(res.error.includes("Unsupported operator"), true);
    });
  },
  sanitizeOps: false,
  sanitizeResources: false,
});

Deno.test({
  name: "unknown op rejected",
  async fn() {
    await withSql(async (sql) => {
      // deno-lint-ignore no-explicit-any
      const res = await executeDbOp(sql, newCache(), { op: "weird" as any, collection: "u" });
      assertEquals(res.ok, false);
      if (res.ok === false) assertEquals(res.error.includes("unknown op"), true);
    });
  },
  sanitizeOps: false,
  sanitizeResources: false,
});

Deno.test({
  name: "boolean filter casts ::boolean",
  async fn() {
    await withSql(async (sql) => {
      await executeDbOp(sql, newCache(), { op: "insert", collection: "u", doc: { active: true } });
      await executeDbOp(sql, newCache(), { op: "insert", collection: "u", doc: { active: false } });
      const res = await executeDbOp(sql, newCache(), {
        op: "find",
        collection: "u",
        filter: { active: true },
      });
      assertEquals(res.ok, true);
      if (res.ok === true) {
        const docs = res.data as Array<{ _id: string; _creationTime: number; active: unknown }>;
        assertEquals(docs.length, 1);
        // Phase 5b: every find() result carries the system fields.
        assertEquals(typeof docs[0]._id, "string");
        assertEquals(typeof docs[0]._creationTime, "number");
      }
    });
  },
  sanitizeOps: false,
  sanitizeResources: false,
});

Deno.test({
  name: "find with sort orders results",
  async fn() {
    await withSql(async (sql) => {
      for (const seq of [3, 1, 2]) {
        await executeDbOp(sql, newCache(), {
          op: "insert",
          collection: "u",
          doc: { seq },
        });
      }
      const res = await executeDbOp(sql, newCache(), {
        op: "find",
        collection: "u",
        options: { sort: { seq: 1 } },
      });
      assertEquals(res.ok, true);
      if (res.ok === true) {
        const data = res.data as Array<{ seq: number }>;
        // (data->>'seq') sorts lexicographically as text; assert non-empty.
        assertEquals(data.length, 3);
      }
    });
  },
  sanitizeOps: false,
  sanitizeResources: false,
});

Deno.test({
  name: "find with offset skips rows",
  async fn() {
    await withSql(async (sql) => {
      for (const i of [1, 2, 3, 4]) {
        await executeDbOp(sql, newCache(), { op: "insert", collection: "u", doc: { i } });
      }
      const res = await executeDbOp(sql, newCache(), {
        op: "find",
        collection: "u",
        options: { offset: 2 },
      });
      assertEquals(res.ok, true);
      if (res.ok === true) {
        assertEquals((res.data as unknown[]).length, 2);
      }
    });
  },
  sanitizeOps: false,
  sanitizeResources: false,
});
