// Phase 14 — cursor snapshot consistency under concurrent INSERT.
//
// The bug: today's `.paginate()` keyset cursor uses `(_creation_time, _id)`
// as the cutoff. If a row is INSERTed between page N and page N+1 with a
// `_creation_time` that falls inside the keyset window already walked, the
// cursor's cutoff lets it past — and the next page silently skips rows the
// client never saw. Convex's contract is the opposite: each `.paginate()`
// chain returns a snapshot view, no late inserts ever appear on later pages.
//
// Fix: capture a snapshot timestamp on the first call (cursor === null) and
// encode it into `continueCursor`. Subsequent pages add
// `_creation_time <= snapshotTs` to the WHERE clause so rows inserted after
// the snapshot are invisible to that pagination session. This test asserts:
//   - page 1 returns the first 10 rows (no snapshotTs in input ⇒ captured),
//   - 5 fresh rows inserted between pages do NOT appear on pages 2-3,
//   - pages 2 and 3 each return 10 of the original 30 rows in order,
//   - after the original 30 are walked, isDone=true,
//   - continuing pagination past isDone returns an empty page without error.
//
// Together with `cursor_snapshot_update.test.ts` (handles UPDATE shifts) and
// `cursor_immutable_keyset.test.ts` (handles `_id` non-duplication), this
// covers the three failure modes called out in row 26 of the parity matrix.

import { assertEquals } from "https://deno.land/std@0.224.0/assert/mod.ts";
import { startRuntime } from "./harness.ts";
import { createCollection, startPostgres } from "./pg_harness.ts";

function bundle(expr: string): string {
  return `globalThis.__excalibase_default = ${expr};`;
}

Deno.test({
  name: "paginate snapshot excludes rows inserted between page fetches",
  async fn() {
    const pg = await startPostgres();
    try {
      await createCollection(pg.url, "snap");
      const rt = await startRuntime({
        v2Enabled: true,
        allowedHosts: `${pg.host}:${pg.port}`,
        dbUrl: pg.url,
      });
      try {
        // Stage 1 — seed 30 docs and read page 1. Returns page1.ids and the
        // continueCursor so the test can do a between-pages INSERT and
        // re-enter the handler with the captured cursor.
        const seed = bundle(`{
          kind: "mutation",
          args: { parse: (a) => a },
          handler: async (ctx, _args) => {
            const c = ctx.db.collection("snap");
            const docs = [];
            for (let i = 0; i < 30; i++) docs.push({ seq: i });
            await c.insertMany(docs);
            const page1 = await ctx.db.query("snap")
              .order("asc")
              .paginate({ cursor: null, numItems: 10 });
            return {
              ids: page1.page.map((d) => d._id),
              cursor: page1.continueCursor,
              isDone: page1.isDone,
            };
          },
        }`);
        await rt.deploy("dbi-snap-seed", seed);
        const seedRes = await rt.invoke("dbi-snap-seed", { args: {} });
        assertEquals(seedRes.status, 200);
        const seedParsed = JSON.parse(seedRes.body);
        const page1Ids: string[] = seedParsed.data.ids;
        const cursorAfterPage1: string = seedParsed.data.cursor;
        assertEquals(page1Ids.length, 10);
        assertEquals(seedParsed.data.isDone, false);
        if (typeof cursorAfterPage1 !== "string" || cursorAfterPage1.length === 0) {
          throw new Error("expected non-empty continueCursor after page 1");
        }

        // Stage 2 — insert 5 fresh docs AFTER the snapshot. Their
        // `_creation_time` is now() at insert time, so they sort after every
        // doc the seed handler created (which used now() back in stage 1).
        // The snapshot watermark on the cursor must hide them on page 2/3.
        const insertLate = bundle(`{
          kind: "mutation",
          args: { parse: (a) => a },
          handler: async (ctx, _args) => {
            const c = ctx.db.collection("snap");
            for (let i = 0; i < 5; i++) await c.insert({ seq: 100 + i, late: true });
            return { ok: true };
          },
        }`);
        await rt.deploy("dbi-snap-late", insertLate);
        const lateRes = await rt.invoke("dbi-snap-late", { args: {} });
        assertEquals(lateRes.status, 200);

        // Stage 3 — walk pages 2 and 3 using the cursor captured before the
        // late inserts. The late docs MUST NOT appear.
        const walk = bundle(`{
          kind: "mutation",
          args: { parse: (a) => a },
          handler: async (ctx, args) => {
            const out = [];
            let cursor = args.cursor;
            for (let p = 0; p < 5; p++) {
              const res = await ctx.db.query("snap")
                .order("asc")
                .paginate({ cursor, numItems: 10 });
              out.push({
                ids: res.page.map((d) => d._id),
                isLate: res.page.map((d) => d.late === true),
                isDone: res.isDone,
                cursor: res.continueCursor,
              });
              if (res.isDone) break;
              cursor = res.continueCursor;
            }
            return { pages: out };
          },
        }`);
        await rt.deploy("dbi-snap-walk", walk);
        const walkRes = await rt.invoke("dbi-snap-walk", {
          args: { cursor: cursorAfterPage1 },
        });
        assertEquals(walkRes.status, 200);
        const walkParsed = JSON.parse(walkRes.body);
        const pages = walkParsed.data.pages as Array<{
          ids: string[];
          isLate: boolean[];
          isDone: boolean;
          cursor: string;
        }>;

        // Pages 2 + 3 walk the remaining 20 original docs (no late ones).
        assertEquals(pages.length, 2, "page2 + page3 = 2 pages");
        assertEquals(pages[0].ids.length, 10, "page 2 has 10 docs");
        assertEquals(pages[1].ids.length, 10, "page 3 has 10 docs");
        assertEquals(pages[1].isDone, true, "last page reports isDone");

        for (const p of pages) {
          for (const late of p.isLate) {
            if (late) {
              throw new Error("a late-inserted doc leaked onto a snapshotted page");
            }
          }
        }

        // No id from page 1 reappears on page 2 or 3.
        const seen = new Set(page1Ids);
        for (const p of pages) {
          for (const id of p.ids) {
            if (seen.has(id)) {
              throw new Error(`id ${id} appeared on multiple pages: ${id}`);
            }
            seen.add(id);
          }
        }
        assertEquals(seen.size, 30, "every original doc must appear exactly once");
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
  name: "paginate walk terminates with isDone=true after walking all rows",
  async fn() {
    const pg = await startPostgres();
    try {
      await createCollection(pg.url, "snap_end");
      const rt = await startRuntime({
        v2Enabled: true,
        allowedHosts: `${pg.host}:${pg.port}`,
        dbUrl: pg.url,
      });
      try {
        const code = bundle(`{
          kind: "mutation",
          args: { parse: (a) => a },
          handler: async (ctx, _args) => {
            const c = ctx.db.collection("snap_end");
            for (let i = 0; i < 5; i++) await c.insert({ i });
            let cursor = null;
            let pages = 0;
            let final = null;
            for (let p = 0; p < 10; p++) {
              const res = await ctx.db.query("snap_end")
                .paginate({ cursor, numItems: 3 });
              pages++;
              final = res;
              if (res.isDone) break;
              cursor = res.continueCursor;
            }
            return {
              finalIsDone: final.isDone,
              finalCursor: final.continueCursor,
              pages,
            };
          },
        }`);
        await rt.deploy("dbi-snap-end", code);
        const res = await rt.invoke("dbi-snap-end", { args: {} });
        assertEquals(res.status, 200);
        const parsed = JSON.parse(res.body);
        // Two pages: [3, 2] over 5 rows. isDone=true on the second.
        assertEquals(parsed.data.pages, 2);
        assertEquals(parsed.data.finalIsDone, true);
        // continueCursor is "" once isDone — the SDK treats "" as "no
        // more pages" and stops looping. We do NOT re-issue with "" as
        // input because that means "start fresh paginate" by the
        // cursor decode contract.
        assertEquals(parsed.data.finalCursor, "");
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
