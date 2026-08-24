// Cursor pagination integration test — exercises the `cursorMode` + `cursor`
// path through `ctx.db.collection(name).find()` end-to-end. Paginates a
// 20-doc collection in 5-doc pages, walks every page, and asserts:
//   * exactly 4 pages
//   * exactly 20 unique ids in total
//   * `nextCursor` is non-null until the last page
//   * `nextCursor` is null on the last page
//
// Pagination contract mirrors `DocumentQueryCompiler.compileFind` on the
// Java side: keyset over `(created_at DESC, id DESC)` so two rows with the
// same timestamp still get a deterministic order via the UUID tiebreaker.
//
// Worker path: the test deploys a v2 mutation that drives `find()` with
// `cursorMode:true`, so we also confirm the worker RPC plumbs the new
// `{docs, nextCursor}` envelope back to user code unchanged.

import { assertEquals } from "https://deno.land/std@0.224.0/assert/mod.ts";
import { startRuntime } from "./harness.ts";
import { createCollection, startPostgres } from "./pg_harness.ts";

function bundle(expr: string): string {
  return `globalThis.__excalibase_default = ${expr};`;
}

Deno.test({
  name: "cursor pagination walks 20 docs in 5-doc pages, returns null cursor on last page",
  async fn() {
    const pg = await startPostgres();
    try {
      await createCollection(pg.url, "pager");
      const rt = await startRuntime({
        v2Enabled: true,
        allowedHosts: `${pg.host}:${pg.port}`,
        dbUrl: pg.url,
      });
      try {
        // The handler does the whole walk in one invocation so we exercise
        // the worker → main RPC for both the insert burst and the four
        // paginated `find` calls. Returning the assembled trace lets the
        // host-side test do clean assertions on shape and contents.
        const code = bundle(`{
          kind: "mutation",
          args: { parse: (a) => a },
          handler: async (ctx, _args) => {
            const c = ctx.db.collection("pager");
            const docs = [];
            for (let i = 0; i < 20; i++) docs.push({ seq: i });
            await c.insertMany(docs);

            const pages = [];
            let cursor = null;
            for (let p = 0; p < 6; p++) {
              const res = await c.find({}, { cursorMode: true, limit: 5, cursor });
              pages.push({ ids: res.docs.map((d) => d._id), nextCursor: res.nextCursor });
              if (res.nextCursor === null) break;
              cursor = res.nextCursor;
            }
            return { pages };
          },
        }`);
        await rt.deploy("dbi-cursor", code);

        const res = await rt.invoke("dbi-cursor", { args: {} });
        assertEquals(res.status, 200);
        const parsed = JSON.parse(res.body);
        const pages = parsed.data.pages as Array<{ ids: string[]; nextCursor: string | null }>;
        assertEquals(pages.length, 4, "exactly 4 pages of 5 rows over 20 docs");
        for (let i = 0; i < 3; i++) {
          assertEquals(pages[i].ids.length, 5);
          if (pages[i].nextCursor === null) {
            throw new Error(`page ${i} reported nextCursor=null but more pages follow`);
          }
        }
        assertEquals(pages[3].ids.length, 5);
        assertEquals(pages[3].nextCursor, null, "last page must have nextCursor=null");

        const seen = new Set<string>();
        for (const p of pages) for (const id of p.ids) seen.add(id);
        assertEquals(seen.size, 20, "every id must appear exactly once across all pages");
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
  name: "cursor pagination with no cursor returns first page + nextCursor when more rows exist",
  async fn() {
    const pg = await startPostgres();
    try {
      await createCollection(pg.url, "kpager");
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
            const c = ctx.db.collection("kpager");
            for (let i = 0; i < 8; i++) await c.insert({ i });
            const page1 = await c.find({}, { cursorMode: true, limit: 3 });
            return { docs: page1.docs.length, nextCursor: page1.nextCursor };
          },
        }`);
        await rt.deploy("dbi-kpager", code);
        const res = await rt.invoke("dbi-kpager", { args: {} });
        assertEquals(res.status, 200);
        const parsed = JSON.parse(res.body);
        assertEquals(parsed.data.docs, 3);
        if (typeof parsed.data.nextCursor !== "string" || parsed.data.nextCursor.length === 0) {
          throw new Error("expected nextCursor to be a non-empty string");
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
  name: "cursor pagination on a collection smaller than limit returns one page with nextCursor=null",
  async fn() {
    const pg = await startPostgres();
    try {
      await createCollection(pg.url, "smol");
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
            const c = ctx.db.collection("smol");
            await c.insertMany([{ i: 1 }, { i: 2 }]);
            const page = await c.find({}, { cursorMode: true, limit: 10 });
            return { count: page.docs.length, nextCursor: page.nextCursor };
          },
        }`);
        await rt.deploy("dbi-smol", code);
        const res = await rt.invoke("dbi-smol", { args: {} });
        const parsed = JSON.parse(res.body);
        assertEquals(parsed.data.count, 2);
        assertEquals(parsed.data.nextCursor, null);
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
