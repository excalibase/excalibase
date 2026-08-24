// Parity sweep — for every CRUD scenario in NoSqlIntegrationTest.java
// (excalibase-nosql Java side), exercise the equivalent flow through
// ctx.db inside a deployed v2 function. Skips search() and vectorSearch()
// — those land in Phase 1.5.
//
// Each test runs in its own Postgres container + runtime to keep
// state-collision impossible. The test runner is allowed to schedule them
// in parallel because the names and ports are randomised.

import { assertEquals, assertExists } from "https://deno.land/std@0.224.0/assert/mod.ts";
import { startRuntime } from "./harness.ts";
import { createCollection, startPostgres } from "./pg_harness.ts";

function bundle(expr: string): string {
  return `globalThis.__excalibase_default = ${expr};`;
}

async function bootEnv(collection: string) {
  const pg = await startPostgres();
  await createCollection(pg.url, collection);
  const rt = await startRuntime({
    v2Enabled: true,
    allowedHosts: `${pg.host}:${pg.port}`,
    dbUrl: pg.url,
  });
  return {
    pg,
    rt,
    cleanup: async () => {
      await rt.stop();
      await pg.stop();
    },
  };
}

Deno.test({
  name: "parity: insertMany inserts every document and assigns distinct _id values",
  async fn() {
    const env = await bootEnv("posts");
    try {
      const code = bundle(`{
        kind: "mutation",
        args: { parse: (a) => a },
        handler: async (ctx, _args) => {
          const c = ctx.db.collection("posts");
          const docs = [
            { title: "Batch 1", status: "new" },
            { title: "Batch 2", status: "new" },
            { title: "Batch 3", status: "new" },
          ];
          return await c.insertMany(docs);
        },
      }`);
      await env.rt.deploy("p-im", code);
      const res = await env.rt.invoke("p-im", { args: {} });
      const parsed = JSON.parse(res.body);
      assertEquals(parsed.data.length, 3);
      const ids = new Set(parsed.data.map((d: { _id: string }) => d._id));
      assertEquals(ids.size, 3, "_id values must be distinct");
      for (const d of parsed.data) {
        // _creationTime is a runtime-assigned millisecond epoch float.
        assertEquals(typeof d._creationTime, "number");
      }
    } finally {
      await env.cleanup();
    }
  },
  sanitizeOps: false,
  sanitizeResources: false,
});

Deno.test({
  name: "parity: $gt / $gte / $lt / $lte numeric operators select expected rows",
  async fn() {
    const env = await bootEnv("nums");
    try {
      const code = bundle(`{
        kind: "mutation",
        args: { parse: (a) => a },
        handler: async (ctx, _args) => {
          const c = ctx.db.collection("nums");
          for (const v of [1, 5, 10, 15, 20]) {
            await c.insert({ value: v });
          }
          const gt = await c.find({ value: { $gt: 5 } });
          const gte = await c.find({ value: { $gte: 5 } });
          const lt = await c.find({ value: { $lt: 10 } });
          const lte = await c.find({ value: { $lte: 10 } });
          return {
            gt: gt.map((r) => r.value).sort((a, b) => a - b),
            gte: gte.map((r) => r.value).sort((a, b) => a - b),
            lt: lt.map((r) => r.value).sort((a, b) => a - b),
            lte: lte.map((r) => r.value).sort((a, b) => a - b),
          };
        },
      }`);
      await env.rt.deploy("p-ops", code);
      const res = await env.rt.invoke("p-ops", { args: {} });
      const parsed = JSON.parse(res.body);
      assertEquals(parsed.data.gt, [10, 15, 20]);
      assertEquals(parsed.data.gte, [5, 10, 15, 20]);
      assertEquals(parsed.data.lt, [1, 5]);
      assertEquals(parsed.data.lte, [1, 5, 10]);
    } finally {
      await env.cleanup();
    }
  },
  sanitizeOps: false,
  sanitizeResources: false,
});

Deno.test({
  name: "parity: $ne and $in operators",
  async fn() {
    const env = await bootEnv("colors");
    try {
      const code = bundle(`{
        kind: "mutation",
        args: { parse: (a) => a },
        handler: async (ctx, _args) => {
          const c = ctx.db.collection("colors");
          for (const v of ["red", "green", "blue", "yellow"]) {
            await c.insert({ name: v });
          }
          const notRed = await c.find({ name: { $ne: "red" } });
          const someColors = await c.find({ name: { $in: ["red", "blue"] } });
          return {
            notRed: notRed.map((r) => r.name).sort(),
            someColors: someColors.map((r) => r.name).sort(),
          };
        },
      }`);
      await env.rt.deploy("p-ne-in", code);
      const res = await env.rt.invoke("p-ne-in", { args: {} });
      const parsed = JSON.parse(res.body);
      assertEquals(parsed.data.notRed, ["blue", "green", "yellow"]);
      assertEquals(parsed.data.someColors, ["blue", "red"]);
    } finally {
      await env.cleanup();
    }
  },
  sanitizeOps: false,
  sanitizeResources: false,
});

Deno.test({
  name: "parity: findOne returns single doc or null",
  async fn() {
    const env = await bootEnv("things");
    try {
      const code = bundle(`{
        kind: "mutation",
        args: { parse: (a) => a },
        handler: async (ctx, _args) => {
          const c = ctx.db.collection("things");
          await c.insert({ name: "alpha" });
          await c.insert({ name: "beta" });
          const found = await c.findOne({ name: "alpha" });
          const missing = await c.findOne({ name: "zeta" });
          return { found, missing };
        },
      }`);
      await env.rt.deploy("p-findone", code);
      const res = await env.rt.invoke("p-findone", { args: {} });
      const parsed = JSON.parse(res.body);
      assertEquals(parsed.data.found.name, "alpha");
      assertEquals(parsed.data.missing, null);
    } finally {
      await env.cleanup();
    }
  },
  sanitizeOps: false,
  sanitizeResources: false,
});

Deno.test({
  name: "parity: update with $set patches matched rows and returns matched/modified counts",
  async fn() {
    const env = await bootEnv("posts");
    try {
      const code = bundle(`{
        kind: "mutation",
        args: { parse: (a) => a },
        handler: async (ctx, _args) => {
          const c = ctx.db.collection("posts");
          await c.insertMany([
            { title: "a", status: "new" },
            { title: "b", status: "new" },
            { title: "c", status: "published" },
          ]);
          const result = await c.update(
            { status: "new" },
            { $set: { status: "archived" } },
          );
          const archived = await c.find({ status: "archived" });
          return {
            matched: result.matched,
            modified: result.modified,
            docs: result.docs.length,
            archivedCount: archived.length,
          };
        },
      }`);
      await env.rt.deploy("p-upd", code);
      const res = await env.rt.invoke("p-upd", { args: {} });
      const parsed = JSON.parse(res.body);
      assertEquals(parsed.data.matched, 2);
      assertEquals(parsed.data.modified, 2);
      assertEquals(parsed.data.docs, 2);
      assertEquals(parsed.data.archivedCount, 2);
    } finally {
      await env.cleanup();
    }
  },
  sanitizeOps: false,
  sanitizeResources: false,
});

Deno.test({
  name: "parity: delete removes matching rows and returns deleted count",
  async fn() {
    const env = await bootEnv("trash");
    try {
      const code = bundle(`{
        kind: "mutation",
        args: { parse: (a) => a },
        handler: async (ctx, _args) => {
          const c = ctx.db.collection("trash");
          await c.insertMany([
            { kind: "old" },
            { kind: "old" },
            { kind: "keep" },
          ]);
          const result = await c.delete({ kind: "old" });
          const remaining = await c.count();
          return { deleted: result.deleted, docs: result.docs.length, remaining };
        },
      }`);
      await env.rt.deploy("p-del", code);
      const res = await env.rt.invoke("p-del", { args: {} });
      const parsed = JSON.parse(res.body);
      assertEquals(parsed.data.deleted, 2);
      assertEquals(parsed.data.docs, 2);
      assertEquals(parsed.data.remaining, 1);
    } finally {
      await env.cleanup();
    }
  },
  sanitizeOps: false,
  sanitizeResources: false,
});

Deno.test({
  name: "parity: count returns 0 on empty filter when no rows exist, total on no filter",
  async fn() {
    const env = await bootEnv("counts");
    try {
      const code = bundle(`{
        kind: "mutation",
        args: { parse: (a) => a },
        handler: async (ctx, _args) => {
          const c = ctx.db.collection("counts");
          const zero = await c.count();
          await c.insertMany([
            { kind: "a" }, { kind: "a" }, { kind: "b" },
          ]);
          const total = await c.count();
          const filtered = await c.count({ kind: "a" });
          return { zero, total, filtered };
        },
      }`);
      await env.rt.deploy("p-cnt", code);
      const res = await env.rt.invoke("p-cnt", { args: {} });
      const parsed = JSON.parse(res.body);
      assertEquals(parsed.data.zero, 0);
      assertEquals(parsed.data.total, 3);
      assertEquals(parsed.data.filtered, 2);
    } finally {
      await env.cleanup();
    }
  },
  sanitizeOps: false,
  sanitizeResources: false,
});

Deno.test({
  name: "parity: insert + getById round-trip, getById of unknown returns null",
  async fn() {
    const env = await bootEnv("rt");
    try {
      const code = bundle(`{
        kind: "mutation",
        args: { parse: (a) => a },
        handler: async (ctx, _args) => {
          const c = ctx.db.collection("rt");
          const inserted = await c.insert({ label: "hello" });
          const fetched = await c.getById(inserted._id);
          const missing = await c.getById("0123456789abcdefghijklmnopqrst");
          return { insertedId: inserted._id, fetchedLabel: fetched && fetched.label, missing };
        },
      }`);
      await env.rt.deploy("p-rt", code);
      const res = await env.rt.invoke("p-rt", { args: {} });
      const parsed = JSON.parse(res.body);
      assertExists(parsed.data.insertedId);
      assertEquals(parsed.data.fetchedLabel, "hello");
      assertEquals(parsed.data.missing, null);
    } finally {
      await env.cleanup();
    }
  },
  sanitizeOps: false,
  sanitizeResources: false,
});

Deno.test({
  name: "parity: find with limit caps result set",
  async fn() {
    const env = await bootEnv("paged");
    try {
      const code = bundle(`{
        kind: "mutation",
        args: { parse: (a) => a },
        handler: async (ctx, _args) => {
          const c = ctx.db.collection("paged");
          for (let i = 0; i < 5; i++) {
            await c.insert({ seq: i });
          }
          const limited = await c.find({}, { limit: 2 });
          return { count: limited.length };
        },
      }`);
      await env.rt.deploy("p-lim", code);
      const res = await env.rt.invoke("p-lim", { args: {} });
      const parsed = JSON.parse(res.body);
      assertEquals(parsed.data.count, 2);
    } finally {
      await env.cleanup();
    }
  },
  sanitizeOps: false,
  sanitizeResources: false,
});

// Phase 1.5: search() and vectorSearch() are no longer stubs — coverage
// for the live implementations lives in dedicated integration suites
// (`search.test.ts`, `vector_search.test.ts`). Removing the placeholder
// assertions here keeps this file focused on the CRUD parity sweep.
