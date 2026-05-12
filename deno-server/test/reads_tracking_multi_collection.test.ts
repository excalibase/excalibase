// Phase 15b — multi-collection reads tracking. A function that reads from
// three different collections must surface all three in the envelope's
// `reads` array. Order is not part of the contract (the graphql registry
// uses Set / Array.includes for the precise-dep match), so we assert
// set-equality, not ordering.
//
// We also cover the various read-op surfaces the worker exposes (find,
// query().collect, count) so every read path properly stamps the active
// txn's reads set — not just the one we covered in the single-collection
// test.

import { assert, assertEquals, assertExists } from "https://deno.land/std@0.224.0/assert/mod.ts";
import { startRuntime } from "./harness.ts";
import { createCollection, startPostgres } from "./pg_harness.ts";

function bundle(expr: string): string {
  return `globalThis.__excalibase_default = ${expr};`;
}

Deno.test({
  name: "phase15b: multi-collection reads (find + query + count) all tracked",
  async fn() {
    const pg = await startPostgres();
    try {
      await createCollection(pg.url, "a");
      await createCollection(pg.url, "b");
      await createCollection(pg.url, "c");
      await createCollection(pg.url, "sink");
      const rt = await startRuntime({
        v2Enabled: true,
        allowedHosts: `${pg.host}:${pg.port}`,
        dbUrl: pg.url,
      });
      try {
        const projectId = "proj_envelope_multi";
        // Three different read surfaces, three different collections, one
        // write to a fourth. Reads must equal the set {a,b,c}.
        await rt.deploy(`${projectId}__multi`, bundle(`{
          kind: "mutation",
          args: { parse: (a) => a },
          handler: async (ctx, args) => {
            const xs = await ctx.db.collection("a").find({});
            const ys = await ctx.db.query("b").collect();
            const n  = await ctx.db.collection("c").count({});
            await ctx.db.collection("sink").insert({ xs: xs.length, ys: ys.length, n });
            return { ok: true };
          },
        }`));

        const res = await rt.invoke(
          `${projectId}__multi`,
          { args: {} },
          { "X-Excalibase-Envelope": "v1" },
        );
        assertEquals(res.status, 200, `body=${res.body}`);
        const parsed = JSON.parse(res.body) as {
          result?: unknown;
          reads?: string[];
        };
        assertExists(parsed.result);
        assert(Array.isArray(parsed.reads));
        const got = new Set(parsed.reads);
        assertEquals(
          got.has("a") && got.has("b") && got.has("c"),
          true,
          `reads must contain a, b, c — got ${JSON.stringify(parsed.reads)}`,
        );
        assertEquals(
          got.has("sink"),
          false,
          `reads must not contain write-only "sink" — got ${JSON.stringify(parsed.reads)}`,
        );
        // No duplicates — the set semantic must survive the wire serialization.
        assertEquals(
          parsed.reads?.length,
          got.size,
          `reads must be a deduped array — got ${JSON.stringify(parsed.reads)}`,
        );
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
  name: "phase15b: repeated reads on same collection deduped in envelope",
  async fn() {
    const pg = await startPostgres();
    try {
      await createCollection(pg.url, "things");
      const rt = await startRuntime({
        v2Enabled: true,
        allowedHosts: `${pg.host}:${pg.port}`,
        dbUrl: pg.url,
      });
      try {
        const projectId = "proj_envelope_dedupe";
        await rt.deploy(`${projectId}__repeated`, bundle(`{
          kind: "query",
          args: { parse: (a) => a },
          handler: async (ctx, args) => {
            await ctx.db.collection("things").find({});
            await ctx.db.collection("things").find({});
            await ctx.db.query("things").collect();
            return await ctx.db.collection("things").count({});
          },
        }`));

        const res = await rt.invoke(
          `${projectId}__repeated`,
          { args: {} },
          { "X-Excalibase-Envelope": "v1" },
        );
        assertEquals(res.status, 200, `body=${res.body}`);
        const parsed = JSON.parse(res.body) as { reads?: string[] };
        assert(Array.isArray(parsed.reads));
        assertEquals(
          parsed.reads,
          ["things"],
          `reads must contain "things" exactly once — got ${JSON.stringify(parsed.reads)}`,
        );
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
