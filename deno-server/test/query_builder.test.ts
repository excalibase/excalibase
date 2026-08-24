// Convex-shape query builder runtime tests.
//
// Phase 6 wires `ctx.db.query(name)` through to a `QueryPlan` compiler on
// the main thread. The worker shim collects the chained calls into a plan
// object and posts it over the existing db RPC with a new `op: "query"`;
// the main thread calls `executeQueryPlan(sql, plan, terminal, extra)`
// which compiles to a single `postgres.js` tagged template.
//
// These tests cover:
//   * terminal methods: first / unique / collect / take / paginate
//   * withIndex with validated index name + bounds
//   * withSearchIndex / withVectorIndex preflight + execution
//   * order asc/desc
//   * MAX_RESULTS enforcement on collect()
//   * end-to-end through a deployed worker (so the RPC plumbing is
//     exercised alongside the SQL compiler)

import { assertEquals } from "https://deno.land/std@0.224.0/assert/mod.ts";
import { startRuntime } from "./harness.ts";
import { createCollection, startPostgres } from "./pg_harness.ts";
import { executeQueryPlan } from "../runtime/db.ts";
import type { QueryPlan } from "../runtime/db.ts";

function bundle(expr: string): string {
  return `globalThis.__excalibase_default = ${expr};`;
}

// withSchemaBundle re-uses the Phase 5b pattern — inject
// `__excalibase_function_metadata.schemaJson` so the worker can preflight
// search/vector index calls against declared indexes.
function withSchemaBundle(schemaJson: unknown, expr: string): string {
  return `
    globalThis.__excalibase_function_metadata = { schemaJson: ${JSON.stringify(schemaJson)} };
    globalThis.__excalibase_default = ${expr};
  `;
}

async function seed(
  pgUrl: string,
  collection: string,
  docs: ReadonlyArray<Record<string, unknown>>,
): Promise<void> {
  const postgres = (await import("npm:postgres@3.4.4")).default;
  const sql = postgres(pgUrl, { onnotice: () => {} });
  try {
    // Tagged-template insert so jsonb is parsed as an object, not stored
    // as a JSON-encoded string (the latter breaks `doc->>'field'` lookups).
    // deno-lint-ignore no-explicit-any
    const sqlAny: any = sql;
    const table = sqlAny.unsafe(`nosql."${collection}"`);
    // Backdate the seeded _creation_time so it is strictly < Date.now()
    // at the moment the test paginate() captures its snapshot watermark.
    // The original `Date.now() + Math.random() * 1000` was forward-biased
    // for ordering variety, but Phase 14's snapshot guard filters by
    // `_creation_time <= snapshotTs` and would exclude every row.
    const seedAt = Date.now() - 5_000;
    let seq = 0;
    for (const d of docs) {
      const _id = `id-${Math.random().toString(36).slice(2, 12)}`;
      const _ct = seedAt + (seq++);
      await sqlAny`
        INSERT INTO ${table} (_id, _creation_time, doc)
        VALUES (${_id}, ${_ct}, ${sqlAny.json(d)})
      `;
    }
  } finally {
    await sql.end({ timeout: 1 });
  }
}

async function withSql<T>(fn: (sql: unknown, pgUrl: string) => Promise<T>): Promise<T> {
  const pg = await startPostgres();
  try {
    const postgres = (await import("npm:postgres@3.4.4")).default;
    const sql = postgres(pg.url, { onnotice: () => {} });
    try {
      // deno-lint-ignore no-explicit-any
      return await fn(sql as any, pg.url);
    } finally {
      await sql.end({ timeout: 1 });
    }
  } finally {
    await pg.stop();
  }
}

// ---------------------------------------------------------------------------
// Unit-level: executeQueryPlan directly.
// ---------------------------------------------------------------------------

Deno.test({
  name: "executeQueryPlan('collect') returns Convex-shape docs with _id and _creationTime",
  async fn() {
    await withSql(async (sql, pgUrl) => {
      await createCollection(pgUrl, "qb_collect");
      await seed(pgUrl, "qb_collect", [{ n: 1 }, { n: 2 }]);
      const plan: QueryPlan = { collection: "qb_collect" };
      const out = (await executeQueryPlan(sql, plan, "collect")) as ReadonlyArray<Record<string, unknown>>;
      assertEquals(out.length, 2);
      for (const d of out) {
        if (typeof d._id !== "string") throw new Error("expected _id string");
        if (typeof d._creationTime !== "number") throw new Error("expected _creationTime number");
      }
    });
  },
  sanitizeOps: false,
  sanitizeResources: false,
});

Deno.test({
  name: "executeQueryPlan('first') returns the first doc or null",
  async fn() {
    await withSql(async (sql, pgUrl) => {
      await createCollection(pgUrl, "qb_first");
      await seed(pgUrl, "qb_first", [{ n: 1 }]);
      const plan: QueryPlan = { collection: "qb_first" };
      const out = await executeQueryPlan(sql, plan, "first");
      if (!out || typeof (out as Record<string, unknown>)._id !== "string") {
        throw new Error("first should return a doc");
      }
    });
  },
  sanitizeOps: false,
  sanitizeResources: false,
});

Deno.test({
  name: "executeQueryPlan('first') on empty collection returns null",
  async fn() {
    await withSql(async (sql, pgUrl) => {
      await createCollection(pgUrl, "qb_first_empty");
      const plan: QueryPlan = { collection: "qb_first_empty" };
      const out = await executeQueryPlan(sql, plan, "first");
      assertEquals(out, null);
    });
  },
  sanitizeOps: false,
  sanitizeResources: false,
});

Deno.test({
  name: "executeQueryPlan('unique') returns the single doc",
  async fn() {
    await withSql(async (sql, pgUrl) => {
      await createCollection(pgUrl, "qb_unique");
      await seed(pgUrl, "qb_unique", [{ n: 1 }]);
      const plan: QueryPlan = { collection: "qb_unique" };
      const out = (await executeQueryPlan(sql, plan, "unique")) as Record<string, unknown>;
      assertEquals(out.n, 1);
    });
  },
  sanitizeOps: false,
  sanitizeResources: false,
});

Deno.test({
  name: "executeQueryPlan('unique') throws when zero matches",
  async fn() {
    await withSql(async (sql, pgUrl) => {
      await createCollection(pgUrl, "qb_unique_0");
      const plan: QueryPlan = { collection: "qb_unique_0" };
      let threw = false;
      try {
        await executeQueryPlan(sql, plan, "unique");
      } catch (e) {
        threw = true;
        if (!String((e as Error).message ?? e).toLowerCase().includes("unique")) {
          throw new Error("error message should mention unique: " + (e as Error).message);
        }
      }
      assertEquals(threw, true);
    });
  },
  sanitizeOps: false,
  sanitizeResources: false,
});

Deno.test({
  name: "executeQueryPlan('unique') throws when more than one matches",
  async fn() {
    await withSql(async (sql, pgUrl) => {
      await createCollection(pgUrl, "qb_unique_n");
      await seed(pgUrl, "qb_unique_n", [{ n: 1 }, { n: 2 }]);
      const plan: QueryPlan = { collection: "qb_unique_n" };
      let threw = false;
      try {
        await executeQueryPlan(sql, plan, "unique");
      } catch (_e) { threw = true; }
      assertEquals(threw, true);
    });
  },
  sanitizeOps: false,
  sanitizeResources: false,
});

Deno.test({
  name: "executeQueryPlan('take', n) caps result at n",
  async fn() {
    await withSql(async (sql, pgUrl) => {
      await createCollection(pgUrl, "qb_take");
      await seed(pgUrl, "qb_take", [{ n: 1 }, { n: 2 }, { n: 3 }, { n: 4 }, { n: 5 }]);
      const plan: QueryPlan = { collection: "qb_take" };
      const out = (await executeQueryPlan(sql, plan, "take", 3)) as ReadonlyArray<unknown>;
      assertEquals(out.length, 3);
    });
  },
  sanitizeOps: false,
  sanitizeResources: false,
});

Deno.test({
  name: "executeQueryPlan('paginate') returns {page, isDone, continueCursor}",
  async fn() {
    await withSql(async (sql, pgUrl) => {
      await createCollection(pgUrl, "qb_page");
      await seed(pgUrl, "qb_page", [{ n: 1 }, { n: 2 }, { n: 3 }, { n: 4 }, { n: 5 }]);
      const plan: QueryPlan = { collection: "qb_page" };
      const out = (await executeQueryPlan(sql, plan, "paginate", {
        cursor: null,
        numItems: 2,
      })) as { page: unknown[]; isDone: boolean; continueCursor: string };
      assertEquals(out.page.length, 2);
      assertEquals(out.isDone, false);
      if (typeof out.continueCursor !== "string" || out.continueCursor.length === 0) {
        throw new Error("expected non-empty continueCursor");
      }
    });
  },
  sanitizeOps: false,
  sanitizeResources: false,
});

Deno.test({
  name: "executeQueryPlan('paginate') sets isDone=true when last page is short",
  async fn() {
    await withSql(async (sql, pgUrl) => {
      await createCollection(pgUrl, "qb_page_done");
      await seed(pgUrl, "qb_page_done", [{ n: 1 }, { n: 2 }]);
      const plan: QueryPlan = { collection: "qb_page_done" };
      const out = (await executeQueryPlan(sql, plan, "paginate", {
        cursor: null,
        numItems: 10,
      })) as { page: unknown[]; isDone: boolean; continueCursor: string };
      assertEquals(out.page.length, 2);
      assertEquals(out.isDone, true);
    });
  },
  sanitizeOps: false,
  sanitizeResources: false,
});

Deno.test({
  name: "executeQueryPlan('collect') throws when result exceeds MAX_RESULTS",
  async fn() {
    await withSql(async (sql, pgUrl) => {
      await createCollection(pgUrl, "qb_max");
      const big: Record<string, unknown>[] = [];
      for (let i = 0; i < 12; i++) big.push({ n: i });
      await seed(pgUrl, "qb_max", big);
      Deno.env.set("EXCALIBASE_QUERY_MAX_RESULTS", "5");
      try {
        const plan: QueryPlan = { collection: "qb_max" };
        let threw = false;
        try {
          await executeQueryPlan(sql, plan, "collect");
        } catch (e) {
          threw = true;
          if (!String((e as Error).message ?? e).toLowerCase().includes("max")) {
            throw new Error("error should mention max: " + (e as Error).message);
          }
        }
        assertEquals(threw, true);
      } finally {
        Deno.env.delete("EXCALIBASE_QUERY_MAX_RESULTS");
      }
    });
  },
  sanitizeOps: false,
  sanitizeResources: false,
});

Deno.test({
  name: "executeQueryPlan order:desc sorts by _creation_time DESC",
  async fn() {
    await withSql(async (sql, pgUrl) => {
      await createCollection(pgUrl, "qb_order");
      const postgres = (await import("npm:postgres@3.4.4")).default;
      const seedSql = postgres(pgUrl, { onnotice: () => {} });
      try {
        const quoted = `nosql."qb_order"`;
        // Insert with controlled creation times so order is deterministic.
        await seedSql.unsafe(
          `INSERT INTO ${quoted} (_id, _creation_time, doc) VALUES
            ('a', 100, '{"label":"first"}'::jsonb),
            ('b', 200, '{"label":"middle"}'::jsonb),
            ('c', 300, '{"label":"last"}'::jsonb)`,
        );
      } finally {
        await seedSql.end({ timeout: 1 });
      }
      const planDesc: QueryPlan = { collection: "qb_order", order: "desc" };
      const desc = (await executeQueryPlan(sql, planDesc, "collect")) as ReadonlyArray<Record<string, unknown>>;
      assertEquals(desc.map((d) => d.label), ["last", "middle", "first"]);
      const planAsc: QueryPlan = { collection: "qb_order", order: "asc" };
      const asc = (await executeQueryPlan(sql, planAsc, "collect")) as ReadonlyArray<Record<string, unknown>>;
      assertEquals(asc.map((d) => d.label), ["first", "middle", "last"]);
    });
  },
  sanitizeOps: false,
  sanitizeResources: false,
});

Deno.test({
  name: "executeQueryPlan withIndex(eq) applies index bounds as WHERE clauses",
  async fn() {
    await withSql(async (sql, pgUrl) => {
      await createCollection(pgUrl, "qb_idx");
      await seed(pgUrl, "qb_idx", [
        { author: "u1", title: "a" },
        { author: "u2", title: "b" },
        { author: "u1", title: "c" },
      ]);
      const plan: QueryPlan = {
        collection: "qb_idx",
        index: { name: "by_author", bounds: [{ field: "author", op: "eq", value: "u1" }] },
      };
      const out = (await executeQueryPlan(sql, plan, "collect")) as ReadonlyArray<Record<string, unknown>>;
      assertEquals(out.length, 2);
      for (const d of out) assertEquals(d.author, "u1");
    });
  },
  sanitizeOps: false,
  sanitizeResources: false,
});

Deno.test({
  name: "executeQueryPlan withIndex(range) applies gte+lte combined bounds",
  async fn() {
    await withSql(async (sql, pgUrl) => {
      await createCollection(pgUrl, "qb_idx_range");
      await seed(pgUrl, "qb_idx_range", [
        { score: 1 }, { score: 5 }, { score: 8 }, { score: 12 },
      ]);
      const plan: QueryPlan = {
        collection: "qb_idx_range",
        index: {
          name: "by_score",
          bounds: [
            { field: "score", op: "gte", value: 5 },
            { field: "score", op: "lte", value: 10 },
          ],
        },
      };
      const out = (await executeQueryPlan(sql, plan, "collect")) as ReadonlyArray<Record<string, unknown>>;
      const sortedScores = out.map((d) => d.score).sort((a, b) => Number(a) - Number(b));
      assertEquals(sortedScores, [5, 8]);
    });
  },
  sanitizeOps: false,
  sanitizeResources: false,
});

Deno.test({
  name: "executeQueryPlan withIndex rejects invalid index field identifiers",
  async fn() {
    await withSql(async (sql, pgUrl) => {
      await createCollection(pgUrl, "qb_inj");
      await seed(pgUrl, "qb_inj", [{ a: 1 }]);
      const plan: QueryPlan = {
        collection: "qb_inj",
        index: { name: "by_a", bounds: [{ field: "a; DROP TABLE qb_inj", op: "eq", value: 1 }] },
      };
      let threw = false;
      try {
        await executeQueryPlan(sql, plan, "collect");
      } catch (_e) { threw = true; }
      assertEquals(threw, true);
    });
  },
  sanitizeOps: false,
  sanitizeResources: false,
});

Deno.test({
  name: "executeQueryPlan withSearchIndex emits FTS WHERE + ts_rank ORDER",
  async fn() {
    await withSql(async (sql, pgUrl) => {
      // Need a search_text generated column for FTS to work.
      const postgres = (await import("npm:postgres@3.4.4")).default;
      const setup = postgres(pgUrl, { onnotice: () => {} });
      try {
        await setup`CREATE SCHEMA IF NOT EXISTS nosql`;
        await setup.unsafe(`
          CREATE TABLE IF NOT EXISTS nosql."qb_search" (
            _id text PRIMARY KEY,
            _creation_time double precision NOT NULL,
            doc jsonb NOT NULL DEFAULT '{}'::jsonb,
            search_body tsvector GENERATED ALWAYS AS (
              to_tsvector('english', coalesce(doc->>'body', ''))
            ) STORED
          )
        `);
        await setup.unsafe(
          `INSERT INTO nosql."qb_search" (_id, _creation_time, doc) VALUES
            ('a', 1, '{"body":"PostgreSQL tsvector and tsquery rock"}'::jsonb),
            ('b', 2, '{"body":"MySQL has its own thing"}'::jsonb),
            ('c', 3, '{"body":"cooking recipes"}'::jsonb)`,
        );
      } finally {
        await setup.end({ timeout: 1 });
      }
      const plan: QueryPlan = {
        collection: "qb_search",
        search: { name: "body_idx", field: "body", query: "tsvector", filters: [] },
      };
      const out = (await executeQueryPlan(sql, plan, "collect")) as ReadonlyArray<Record<string, unknown>>;
      if (out.length === 0) throw new Error("expected at least one hit");
      assertEquals((out[0].body as string).includes("tsvector"), true);
    });
  },
  sanitizeOps: false,
  sanitizeResources: false,
});

// ---------------------------------------------------------------------------
// E2E through the worker — proves the RPC plumbing routes the new op too.
// ---------------------------------------------------------------------------

Deno.test({
  name: "ctx.db.query('users').collect() round-trips through the worker",
  async fn() {
    const pg = await startPostgres();
    try {
      await createCollection(pg.url, "qb_e2e_users");
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
            const c = ctx.db.collection("qb_e2e_users");
            await c.insertMany([
              { name: "alice", votes: 5 },
              { name: "bob", votes: 12 },
            ]);
            const docs = await ctx.db.query("qb_e2e_users")
              .filter((q) => q.gt(q.field("votes"), 10))
              .collect();
            return docs.map((d) => d.name);
          },
        }`);
        await rt.deploy("qb-e2e-collect", code);
        const res = await rt.invoke("qb-e2e-collect", { args: {} });
        const parsed = JSON.parse(res.body);
        assertEquals(parsed.data, ["bob"]);
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
  name: "ctx.db.query(name).first / unique / take work through the worker",
  async fn() {
    const pg = await startPostgres();
    try {
      await createCollection(pg.url, "qb_e2e_terms");
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
            const c = ctx.db.collection("qb_e2e_terms");
            await c.insertMany([
              { tag: "x", n: 1 }, { tag: "y", n: 2 }, { tag: "z", n: 3 },
            ]);
            const q = ctx.db.query("qb_e2e_terms");
            const first = await q.first();
            const take2 = await q.take(2);
            // unique with a tight filter — exactly one row matches.
            const u = await ctx.db.query("qb_e2e_terms").filter((f) => f.eq(f.field("tag"), "y")).unique();
            return { firstOk: first !== null, takeCount: take2.length, uniqueTag: u.tag };
          },
        }`);
        await rt.deploy("qb-e2e-terms", code);
        const res = await rt.invoke("qb-e2e-terms", { args: {} });
        const parsed = JSON.parse(res.body);
        assertEquals(parsed.data.firstOk, true);
        assertEquals(parsed.data.takeCount, 2);
        assertEquals(parsed.data.uniqueTag, "y");
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
  name: "ctx.db.query(name).paginate walks the collection in pages",
  async fn() {
    const pg = await startPostgres();
    try {
      await createCollection(pg.url, "qb_e2e_page");
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
            const c = ctx.db.collection("qb_e2e_page");
            const docs = [];
            for (let i = 0; i < 8; i++) docs.push({ seq: i });
            await c.insertMany(docs);
            const pages = [];
            let cursor = null;
            for (let i = 0; i < 10; i++) {
              const r = await ctx.db.query("qb_e2e_page").paginate({ cursor, numItems: 3 });
              pages.push({ count: r.page.length, isDone: r.isDone });
              if (r.isDone) break;
              cursor = r.continueCursor;
            }
            return pages;
          },
        }`);
        await rt.deploy("qb-e2e-page", code);
        const res = await rt.invoke("qb-e2e-page", { args: {} });
        const parsed = JSON.parse(res.body);
        // 8 docs / 3 per page => pages of [3, 3, 2], last isDone=true
        assertEquals(parsed.data.length, 3);
        assertEquals(parsed.data[0], { count: 3, isDone: false });
        assertEquals(parsed.data[1], { count: 3, isDone: false });
        assertEquals(parsed.data[2], { count: 2, isDone: true });
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
  name: "ctx.db.query(name).withSearchIndex preflights against declared searchIndex",
  async fn() {
    const pg = await startPostgres();
    try {
      // Add a search column to the table since the worker only checks the
      // schema metadata, not the column's actual existence.
      const postgres = (await import("npm:postgres@3.4.4")).default;
      const setup = postgres(pg.url, { onnotice: () => {} });
      try {
        await setup`CREATE SCHEMA IF NOT EXISTS nosql`;
        await setup.unsafe(`
          CREATE TABLE IF NOT EXISTS nosql."qb_e2e_search" (
            _id text PRIMARY KEY,
            _creation_time double precision NOT NULL,
            doc jsonb NOT NULL DEFAULT '{}'::jsonb,
            search_body tsvector GENERATED ALWAYS AS (
              to_tsvector('english', coalesce(doc->>'body', ''))
            ) STORED
          )
        `);
      } finally {
        await setup.end({ timeout: 1 });
      }
      const rt = await startRuntime({
        v2Enabled: true,
        allowedHosts: `${pg.host}:${pg.port}`,
        dbUrl: pg.url,
      });
      try {
        const schema = {
          tables: {
            qb_e2e_search: {
              validator: { type: "object", properties: {}, required: [], additionalProperties: true },
              indexes: [],
              searchIndexes: [{ name: "body_idx", searchField: "body", filterFields: [] }],
              vectorIndexes: [],
            },
          },
        };
        const code = withSchemaBundle(schema, `{
          kind: "mutation",
          args: { parse: (a) => a },
          handler: async (ctx, _args) => {
            const c = ctx.db.collection("qb_e2e_search");
            await c.insertMany([
              { body: "PostgreSQL tsvector and tsquery rule" },
              { body: "MySQL has its own thing" },
            ]);
            const hits = await ctx.db.query("qb_e2e_search")
              .withSearchIndex("body_idx", (s) => s.search("body", "tsvector"))
              .collect();
            return hits.map((d) => d.body);
          },
        }`);
        await rt.deploy("qb-e2e-search", code);
        const res = await rt.invoke("qb-e2e-search", { args: {} });
        const parsed = JSON.parse(res.body);
        if (!Array.isArray(parsed.data) || parsed.data.length === 0) {
          throw new Error("expected hits: " + JSON.stringify(parsed));
        }
        if (!String(parsed.data[0]).includes("tsvector")) {
          throw new Error("first hit should mention tsvector: " + parsed.data[0]);
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
  name: "ctx.db.query(name).withSearchIndex preflights to a friendly error when index not declared",
  async fn() {
    const pg = await startPostgres();
    try {
      await createCollection(pg.url, "qb_e2e_nosearch");
      const rt = await startRuntime({
        v2Enabled: true,
        allowedHosts: `${pg.host}:${pg.port}`,
        dbUrl: pg.url,
      });
      try {
        const schema = {
          tables: {
            qb_e2e_nosearch: {
              validator: { type: "object", properties: {}, required: [], additionalProperties: true },
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
              await ctx.db.query("qb_e2e_nosearch")
                .withSearchIndex("body_idx", (s) => s.search("body", "anything"))
                .collect();
              return { ok: true };
            } catch (e) {
              return { error: String(e && e.message || e) };
            }
          },
        }`);
        await rt.deploy("qb-e2e-nosearch", code);
        const res = await rt.invoke("qb-e2e-nosearch", { args: {} });
        const parsed = JSON.parse(res.body);
        if (parsed.data?.ok === true) throw new Error("should have thrown");
        if (!parsed.data.error.toLowerCase().includes("search")) {
          throw new Error("error should mention search: " + parsed.data.error);
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
