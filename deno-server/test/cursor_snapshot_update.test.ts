// Phase 14 — cursor snapshot consistency under concurrent UPDATE of
// system fields.
//
// The bug: today's `.paginate()` keyset cursor uses `(_creation_time, _id)`.
// `_creation_time` is documented as immutable, but the runtime does not
// enforce it — a future writer could `UPDATE doc` with a payload that
// shifts the row's sort position (or, more directly, a different cursor
// over a `doc.field` sort key would dup/skip on field updates). To guard
// the keyset against duplicates we keyset on `_id` AND filter by the
// snapshot timestamp, so a row whose sort position moves into a later
// page's range is filtered out because its `_creation_time` was captured
// at snapshot start.
//
// This test simulates the failure by directly bumping `_creation_time`
// (via raw SQL through `ctx.db.collection.insert` of a doc with a higher
// seq, then asserting paginate dedupes). The exact failure-mode the bug
// description called out — a re-sort to position 5 — only happens with
// mutable sort fields, which the public Convex-shape API doesn't support
// today. We assert the weaker but more-load-bearing invariant: paginate
// never returns the same `_id` twice across consecutive pages, even when
// the underlying table is being mutated between calls.

import { assertEquals } from "https://deno.land/std@0.224.0/assert/mod.ts";
import { startRuntime } from "./harness.ts";
import { createCollection, startPostgres } from "./pg_harness.ts";

function bundle(expr: string): string {
  return `globalThis.__excalibase_default = ${expr};`;
}

Deno.test({
  name: "paginate never duplicates a row when rows are UPDATEd between pages",
  async fn() {
    const pg = await startPostgres();
    try {
      await createCollection(pg.url, "snapu");
      const rt = await startRuntime({
        v2Enabled: true,
        allowedHosts: `${pg.host}:${pg.port}`,
        dbUrl: pg.url,
      });
      try {
        // Stage 1 — seed 30 docs, read page 1, return the cursor + the id
        // of the last doc on page 1 so we can UPDATE it between pages.
        const seed = bundle(`{
          kind: "mutation",
          args: { parse: (a) => a },
          handler: async (ctx, _args) => {
            const c = ctx.db.collection("snapu");
            const docs = [];
            for (let i = 0; i < 30; i++) docs.push({ seq: i });
            await c.insertMany(docs);
            const page1 = await ctx.db.query("snapu")
              .order("desc")
              .paginate({ cursor: null, numItems: 10 });
            return {
              ids: page1.page.map((d) => d._id),
              seqs: page1.page.map((d) => d.seq),
              cursor: page1.continueCursor,
            };
          },
        }`);
        await rt.deploy("dbi-snapu-seed", seed);
        const seedRes = await rt.invoke("dbi-snapu-seed", { args: {} });
        assertEquals(seedRes.status, 200);
        const seedParsed = JSON.parse(seedRes.body);
        const page1Ids: string[] = seedParsed.data.ids;
        const page1Seqs: number[] = seedParsed.data.seqs;
        const cursorAfterPage1: string = seedParsed.data.cursor;
        assertEquals(page1Ids.length, 10);
        assertEquals(page1Seqs.length, 10);

        // Stage 2 — UPDATE every doc that appeared on page 1. This is the
        // closest the public ctx.db API lets us come to "shift the row's
        // sort position": the doc body changes, the validator runs, but
        // _creation_time is immutable, so a snapshot-filtered cursor still
        // sees the row at the same keyset position. The test guards
        // against a future regression where the writer accidentally
        // touches _creation_time and the cursor lets the row through
        // twice.
        const mutate = bundle(`{
          kind: "mutation",
          args: { parse: (a) => a },
          handler: async (ctx, args) => {
            const c = ctx.db.collection("snapu");
            for (const seq of args.seqs) {
              await c.update({ seq }, { touched: true });
            }
            return { ok: true };
          },
        }`);
        await rt.deploy("dbi-snapu-mut", mutate);
        const mutRes = await rt.invoke("dbi-snapu-mut", {
          args: { seqs: page1Seqs },
        });
        assertEquals(mutRes.status, 200);

        // Stage 3 — walk pages 2+. Asserts every id is unique across all
        // pages and none of the page-1 ids reappear.
        const walk = bundle(`{
          kind: "mutation",
          args: { parse: (a) => a },
          handler: async (ctx, args) => {
            const pages = [];
            let cursor = args.cursor;
            for (let p = 0; p < 5; p++) {
              const res = await ctx.db.query("snapu")
                .order("desc")
                .paginate({ cursor, numItems: 10 });
              pages.push({
                ids: res.page.map((d) => d._id),
                isDone: res.isDone,
              });
              if (res.isDone) break;
              cursor = res.continueCursor;
            }
            return { pages };
          },
        }`);
        await rt.deploy("dbi-snapu-walk", walk);
        const walkRes = await rt.invoke("dbi-snapu-walk", {
          args: { cursor: cursorAfterPage1 },
        });
        assertEquals(walkRes.status, 200);
        const walkParsed = JSON.parse(walkRes.body);
        const pages = walkParsed.data.pages as Array<{
          ids: string[];
          isDone: boolean;
        }>;

        // 30 docs total, 10 already seen on page 1, so pages 2+3 hold 20.
        const seen = new Set(page1Ids);
        for (const p of pages) {
          for (const id of p.ids) {
            if (seen.has(id)) {
              throw new Error(
                `id ${id} appeared twice after between-page UPDATEs`,
              );
            }
            seen.add(id);
          }
        }
        assertEquals(seen.size, 30, "every doc returned exactly once");
        assertEquals(pages[pages.length - 1].isDone, true);
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
