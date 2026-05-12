// Phase 14 — keyset-on-_id never duplicates a row regardless of UPDATEs.
//
// `_id` is the immutable primary key; the runtime never lets user code
// patch it (the `update` op merges `doc || patch` and skips the system
// columns). The cursor's tiebreaker uses `_id`, so two pages of the
// same chain MUST never return the same `_id` — even when the underlying
// `doc` is being patched between pages and the row's text representation
// in `doc` changes wildly.
//
// This is a tighter assertion than `cursor_snapshot.test.ts` (which
// covers INSERTs) and `cursor_snapshot_update.test.ts` (which covers
// UPDATEs that don't shift the keyset). Here we hammer the row body
// in a loop between every page fetch and assert the cursor stays
// well-formed: 50 docs, 10-per-page, ids are unique across all 5
// pages, no doc skipped.

import { assertEquals } from "https://deno.land/std@0.224.0/assert/mod.ts";
import { startRuntime } from "./harness.ts";
import { createCollection, startPostgres } from "./pg_harness.ts";

function bundle(expr: string): string {
  return `globalThis.__excalibase_default = ${expr};`;
}

Deno.test({
  name: "paginate keyset on _id never duplicates rows under UPDATE churn",
  async fn() {
    const pg = await startPostgres();
    try {
      await createCollection(pg.url, "imm");
      const rt = await startRuntime({
        v2Enabled: true,
        allowedHosts: `${pg.host}:${pg.port}`,
        dbUrl: pg.url,
      });
      try {
        // Seed 50 docs with unique `seq` so we can patch each by filter.
        const seed = bundle(`{
          kind: "mutation",
          args: { parse: (a) => a },
          handler: async (ctx, _args) => {
            const c = ctx.db.collection("imm");
            const docs = [];
            for (let i = 0; i < 50; i++) docs.push({ seq: i, rev: 0 });
            await c.insertMany(docs);
            return { ok: true };
          },
        }`);
        await rt.deploy("dbi-imm-seed", seed);
        const seedRes = await rt.invoke("dbi-imm-seed", { args: {} });
        assertEquals(seedRes.status, 200);

        // One-shot handler that takes (cursor, churnSeqs) and:
        //   - patches each churnSeqs row (`rev = 1`),
        //   - reads the next page using the cursor,
        //   - returns the page's ids + the next cursor.
        const step = bundle(`{
          kind: "mutation",
          args: { parse: (a) => a },
          handler: async (ctx, args) => {
            const c = ctx.db.collection("imm");
            for (const seq of args.churnSeqs) {
              await c.update({ seq }, { rev: 1 });
            }
            const page = await ctx.db.query("imm")
              .order("asc")
              .paginate({ cursor: args.cursor, numItems: 10 });
            return {
              ids: page.page.map((d) => d._id),
              isDone: page.isDone,
              cursor: page.continueCursor,
            };
          },
        }`);
        await rt.deploy("dbi-imm-step", step);

        // Walk five pages with random per-page churn against the seq space.
        const seen = new Set<string>();
        let cursor: string | null = null;
        for (let p = 0; p < 6; p++) {
          const churn: number[] = [];
          for (let i = 0; i < 5; i++) {
            churn.push(Math.floor(Math.random() * 50));
          }
          const res = await rt.invoke("dbi-imm-step", {
            args: { cursor, churnSeqs: churn },
          });
          assertEquals(res.status, 200);
          const parsed = JSON.parse(res.body);
          for (const id of parsed.data.ids) {
            if (seen.has(id)) {
              throw new Error(
                `id ${id} returned twice across paginate calls (page ${p})`,
              );
            }
            seen.add(id);
          }
          if (parsed.data.isDone) break;
          cursor = parsed.data.cursor;
        }
        assertEquals(seen.size, 50, "all 50 docs walked exactly once");
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
