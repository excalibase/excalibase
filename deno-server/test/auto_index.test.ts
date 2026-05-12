// Phase 6.5 — auto-index selection.
//
// When the user writes `ctx.db.query("posts").filter(q.eq("author", uid))`
// and a declared index `by_author` on `["author"]` exists, the runtime
// should detect the prefix match and apply the index transparently.
//
// Coverage split:
//   * Pure unit tests on `selectIndex(plan, indexes)` — no DB needed.
//   * Integration via `executeQueryPlan` with the auto-selected index
//     applied — verifies SQL stays correct.
//   * E2E through the worker — verifies the worker shim plumbs the
//     schema-aware selection end-to-end.
//
// EXPLAIN-based verification lives in `auto_index_explain.test.ts`.

import { assertEquals } from "https://deno.land/std@0.224.0/assert/mod.ts";
import { selectIndex } from "../runtime/index_selector.ts";
import type { QueryPlan, FilterExpr } from "../runtime/db.ts";
import type { IndexDef } from "../runtime/schema.ts";
import { executeQueryPlan } from "../runtime/db.ts";
import { startPostgres, createCollection } from "./pg_harness.ts";
import { startRuntime } from "./harness.ts";

// ---------------------------------------------------------------------------
// Pure-function unit tests for selectIndex.
// ---------------------------------------------------------------------------

function eq(field: string, value: unknown): FilterExpr {
  return { kind: "eq", left: { kind: "field", name: field }, right: value };
}

function and(...args: FilterExpr[]): FilterExpr {
  return { kind: "and", args };
}

Deno.test("selectIndex: single eq matches single-field index", () => {
  const plan: QueryPlan = {
    collection: "posts",
    filter: eq("author", "u1"),
  };
  const indexes: readonly IndexDef[] = [{ name: "by_author", fields: ["author"] }];
  const sel = selectIndex(plan, indexes);
  if (sel === null) throw new Error("expected non-null selection");
  assertEquals(sel.name, "by_author");
  assertEquals(sel.bounds.length, 1);
  assertEquals(sel.bounds[0].field, "author");
  assertEquals(sel.bounds[0].op, "eq");
  assertEquals(sel.bounds[0].value, "u1");
});

Deno.test("selectIndex: composite index with both fields eq-bound", () => {
  const plan: QueryPlan = {
    collection: "posts",
    filter: and(eq("author", "u1"), eq("status", "published")),
  };
  const indexes: readonly IndexDef[] = [
    { name: "by_author_status", fields: ["author", "status"] },
  ];
  const sel = selectIndex(plan, indexes);
  if (sel === null) throw new Error("expected non-null selection");
  assertEquals(sel.name, "by_author_status");
  assertEquals(sel.bounds.length, 2);
  assertEquals(sel.bounds[0].field, "author");
  assertEquals(sel.bounds[1].field, "status");
});

Deno.test("selectIndex: composite index usable on leading-prefix-only filter", () => {
  // Convex semantics: a composite [a,b,c] index IS usable when only `a`
  // is eq-bound (b-tree prefix match). The selector returns one bound
  // matching the leading covered field.
  const plan: QueryPlan = {
    collection: "posts",
    filter: eq("author", "u1"),
  };
  const indexes: readonly IndexDef[] = [
    { name: "by_author_status", fields: ["author", "status"] },
  ];
  const sel = selectIndex(plan, indexes);
  if (sel === null) throw new Error("expected non-null selection on prefix");
  assertEquals(sel.name, "by_author_status");
  assertEquals(sel.bounds.length, 1);
  assertEquals(sel.bounds[0].field, "author");
});

Deno.test("selectIndex: no match when filter field has no index", () => {
  const plan: QueryPlan = {
    collection: "posts",
    filter: eq("title", "hello"),
  };
  const indexes: readonly IndexDef[] = [{ name: "by_author", fields: ["author"] }];
  const sel = selectIndex(plan, indexes);
  assertEquals(sel, null);
});

Deno.test("selectIndex: no match when no filter", () => {
  const plan: QueryPlan = { collection: "posts" };
  const indexes: readonly IndexDef[] = [{ name: "by_author", fields: ["author"] }];
  const sel = selectIndex(plan, indexes);
  assertEquals(sel, null);
});

Deno.test("selectIndex: no match when no indexes", () => {
  const plan: QueryPlan = { collection: "posts", filter: eq("author", "u1") };
  const sel = selectIndex(plan, []);
  assertEquals(sel, null);
});

Deno.test("selectIndex: longest-prefix wins between two candidate indexes", () => {
  // Both indexes cover the leading `author` eq. The composite covers MORE
  // (author + status), so it wins.
  const plan: QueryPlan = {
    collection: "posts",
    filter: and(eq("author", "u1"), eq("status", "published")),
  };
  const indexes: readonly IndexDef[] = [
    { name: "by_author", fields: ["author"] },
    { name: "by_author_status", fields: ["author", "status"] },
  ];
  const sel = selectIndex(plan, indexes);
  if (sel === null) throw new Error("expected non-null");
  assertEquals(sel.name, "by_author_status");
  assertEquals(sel.bounds.length, 2);
});

Deno.test("selectIndex: tie-break by lex order on index name", () => {
  // Two indexes both cover exactly 1 field. Sorted lexicographically,
  // "a_idx" < "z_idx".
  const plan: QueryPlan = {
    collection: "posts",
    filter: eq("author", "u1"),
  };
  const indexes: readonly IndexDef[] = [
    { name: "z_idx", fields: ["author"] },
    { name: "a_idx", fields: ["author"] },
  ];
  const sel = selectIndex(plan, indexes);
  if (sel === null) throw new Error("expected non-null");
  assertEquals(sel.name, "a_idx");
});

Deno.test("selectIndex: explicit index hint disables auto-selection (caller decides)", () => {
  // The selector itself is pure; the caller (worker / executeQueryPlan)
  // is responsible for skipping when plan.index is set. Document the
  // contract by asserting selectIndex returns the same answer either
  // way — the SKIP must happen upstream.
  const plan: QueryPlan = {
    collection: "posts",
    index: { name: "manual", bounds: [{ field: "author", op: "eq", value: "u1" }] },
    filter: eq("author", "u1"),
  };
  const indexes: readonly IndexDef[] = [{ name: "by_author", fields: ["author"] }];
  // Caller MUST guard; selectIndex returns the auto answer regardless.
  const sel = selectIndex(plan, indexes);
  if (sel === null) throw new Error("expected non-null");
  assertEquals(sel.name, "by_author");
});

Deno.test("selectIndex: AND with non-eq leaf still extracts eq", () => {
  // filter = (author = u1) AND (votes > 10). Only the leading eq is
  // usable for the index; the gt becomes a post-filter (still in plan.filter).
  const plan: QueryPlan = {
    collection: "posts",
    filter: and(
      eq("author", "u1"),
      { kind: "gt", left: { kind: "field", name: "votes" }, right: 10 },
    ),
  };
  const indexes: readonly IndexDef[] = [{ name: "by_author", fields: ["author"] }];
  const sel = selectIndex(plan, indexes);
  if (sel === null) throw new Error("expected non-null");
  assertEquals(sel.name, "by_author");
  assertEquals(sel.bounds.length, 1);
});

Deno.test("selectIndex: filter wrapped in OR is not eligible for auto-selection", () => {
  // `(author = u1) OR (author = u2)` does not give a single leading eq —
  // it's a disjunction. We DO NOT auto-pick.
  const plan: QueryPlan = {
    collection: "posts",
    filter: { kind: "or", args: [eq("author", "u1"), eq("author", "u2")] },
  };
  const indexes: readonly IndexDef[] = [{ name: "by_author", fields: ["author"] }];
  const sel = selectIndex(plan, indexes);
  assertEquals(sel, null);
});

// ---------------------------------------------------------------------------
// Integration: executeQueryPlan with an externally-applied auto-selected
// index. We don't have schema visibility here, so we simulate what the
// worker does: run `selectIndex`, set `plan.index`, set `plan.autoSelectedIndex`,
// then run executeQueryPlan. The point is to verify the resulting SQL
// runs and returns the right rows.
// ---------------------------------------------------------------------------

async function seed(
  pgUrl: string,
  collection: string,
  docs: ReadonlyArray<Record<string, unknown>>,
): Promise<void> {
  const postgres = (await import("npm:postgres@3.4.4")).default;
  const sql = postgres(pgUrl, { onnotice: () => {} });
  try {
    // deno-lint-ignore no-explicit-any
    const sqlAny: any = sql;
    const table = sqlAny.unsafe(`nosql."${collection}"`);
    // Backdate the seeded _creation_time so it is strictly < Date.now()
    // at the moment any test paginate() captures its Phase 14 snapshot
    // watermark. The original `+ Math.random() * 1000` was forward-biased
    // for ordering variety.
    const seedAt = Date.now() - 5_000;
    let seq = 0;
    for (const d of docs) {
      const id = `id-${Math.random().toString(36).slice(2, 12)}`;
      const ct = seedAt + (seq++);
      await sqlAny`
        INSERT INTO ${table} (_id, _creation_time, doc)
        VALUES (${id}, ${ct}, ${sqlAny.json(d)})
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

function applyAutoIndex(plan: QueryPlan, indexes: readonly IndexDef[]): QueryPlan {
  // Mirror the worker contract: only auto-select when no explicit index.
  if (plan.index) return plan;
  const sel = selectIndex(plan, indexes);
  if (sel === null) return plan;
  return { ...plan, index: sel, autoSelectedIndex: sel.name };
}

Deno.test({
  name: "auto-index: single-field index applied at executeQueryPlan returns matching rows",
  async fn() {
    await withSql(async (sql, pgUrl) => {
      await createCollection(pgUrl, "ai_posts");
      await seed(pgUrl, "ai_posts", [
        { author: "u1", title: "a" },
        { author: "u2", title: "b" },
        { author: "u1", title: "c" },
      ]);
      const basePlan: QueryPlan = {
        collection: "ai_posts",
        filter: eq("author", "u1"),
      };
      const indexes: readonly IndexDef[] = [{ name: "by_author", fields: ["author"] }];
      const finalPlan = applyAutoIndex(basePlan, indexes);
      assertEquals(finalPlan.autoSelectedIndex, "by_author");
      const out = (await executeQueryPlan(sql, finalPlan, "collect")) as ReadonlyArray<Record<string, unknown>>;
      assertEquals(out.length, 2);
      for (const d of out) assertEquals(d.author, "u1");
    });
  },
  sanitizeOps: false,
  sanitizeResources: false,
});

Deno.test({
  name: "auto-index: composite index applied with both eq bounds returns the intersection",
  async fn() {
    await withSql(async (sql, pgUrl) => {
      await createCollection(pgUrl, "ai_posts2");
      await seed(pgUrl, "ai_posts2", [
        { author: "u1", status: "published", title: "a" },
        { author: "u1", status: "draft",     title: "b" },
        { author: "u2", status: "published", title: "c" },
      ]);
      const basePlan: QueryPlan = {
        collection: "ai_posts2",
        filter: and(eq("author", "u1"), eq("status", "published")),
      };
      const indexes: readonly IndexDef[] = [
        { name: "by_author_status", fields: ["author", "status"] },
      ];
      const finalPlan = applyAutoIndex(basePlan, indexes);
      assertEquals(finalPlan.autoSelectedIndex, "by_author_status");
      const out = (await executeQueryPlan(sql, finalPlan, "collect")) as ReadonlyArray<Record<string, unknown>>;
      assertEquals(out.length, 1);
      assertEquals(out[0].title, "a");
    });
  },
  sanitizeOps: false,
  sanitizeResources: false,
});

Deno.test({
  name: "auto-index: no match leaves the plan untouched (no autoSelectedIndex)",
  async fn() {
    await withSql(async (sql, pgUrl) => {
      await createCollection(pgUrl, "ai_posts3");
      await seed(pgUrl, "ai_posts3", [{ title: "a" }, { title: "b" }]);
      const basePlan: QueryPlan = {
        collection: "ai_posts3",
        filter: eq("title", "a"),
      };
      const indexes: readonly IndexDef[] = [{ name: "by_author", fields: ["author"] }];
      const finalPlan = applyAutoIndex(basePlan, indexes);
      assertEquals(finalPlan.autoSelectedIndex, undefined);
      assertEquals(finalPlan.index, undefined);
      const out = (await executeQueryPlan(sql, finalPlan, "collect")) as ReadonlyArray<Record<string, unknown>>;
      assertEquals(out.length, 1);
    });
  },
  sanitizeOps: false,
  sanitizeResources: false,
});

Deno.test({
  name: "auto-index: explicit withIndex is preserved (no override)",
  async fn() {
    await withSql(async (sql, pgUrl) => {
      await createCollection(pgUrl, "ai_posts4");
      await seed(pgUrl, "ai_posts4", [
        { author: "u1", title: "a" },
        { author: "u2", title: "b" },
      ]);
      const basePlan: QueryPlan = {
        collection: "ai_posts4",
        // User picked an index by name. Auto-selector must NOT overwrite.
        index: { name: "explicit_idx", bounds: [{ field: "author", op: "eq", value: "u1" }] },
        filter: eq("author", "u1"),
      };
      const indexes: readonly IndexDef[] = [{ name: "by_author", fields: ["author"] }];
      const finalPlan = applyAutoIndex(basePlan, indexes);
      // index untouched; autoSelectedIndex stays undefined.
      assertEquals(finalPlan.index?.name, "explicit_idx");
      assertEquals(finalPlan.autoSelectedIndex, undefined);
      const out = (await executeQueryPlan(sql, finalPlan, "collect")) as ReadonlyArray<Record<string, unknown>>;
      assertEquals(out.length, 1);
      assertEquals(out[0].author, "u1");
    });
  },
  sanitizeOps: false,
  sanitizeResources: false,
});

Deno.test({
  name: "auto-index: order('desc') still applies (auto-selection does not override order)",
  async fn() {
    await withSql(async (sql, pgUrl) => {
      await createCollection(pgUrl, "ai_posts_order");
      // Use controlled creation times for deterministic ordering.
      const postgres = (await import("npm:postgres@3.4.4")).default;
      const setup = postgres(pgUrl, { onnotice: () => {} });
      try {
        await setup.unsafe(
          `INSERT INTO nosql."ai_posts_order" (_id, _creation_time, doc) VALUES
            ('a', 100, '{"author":"u1","label":"first"}'::jsonb),
            ('b', 200, '{"author":"u1","label":"middle"}'::jsonb),
            ('c', 300, '{"author":"u1","label":"last"}'::jsonb)`,
        );
      } finally {
        await setup.end({ timeout: 1 });
      }
      const basePlan: QueryPlan = {
        collection: "ai_posts_order",
        filter: eq("author", "u1"),
        order: "desc",
      };
      const indexes: readonly IndexDef[] = [{ name: "by_author", fields: ["author"] }];
      const finalPlan = applyAutoIndex(basePlan, indexes);
      assertEquals(finalPlan.autoSelectedIndex, "by_author");
      const out = (await executeQueryPlan(sql, finalPlan, "collect")) as ReadonlyArray<Record<string, unknown>>;
      assertEquals(out.map((d) => d.label), ["last", "middle", "first"]);
    });
  },
  sanitizeOps: false,
  sanitizeResources: false,
});

// ---------------------------------------------------------------------------
// E2E through the worker — proves the worker shim runs the selector,
// stamps `autoSelectedIndex` onto the plan, and the main thread receives it.
// ---------------------------------------------------------------------------

function withSchemaBundle(schemaJson: unknown, expr: string): string {
  return `
    globalThis.__excalibase_function_metadata = { schemaJson: ${JSON.stringify(schemaJson)} };
    globalThis.__excalibase_default = ${expr};
  `;
}

Deno.test({
  name: "E2E: ctx.db.query.filter auto-selects declared index when schema is present",
  async fn() {
    const pg = await startPostgres();
    try {
      await createCollection(pg.url, "ai_e2e_posts");
      const rt = await startRuntime({
        v2Enabled: true,
        allowedHosts: `${pg.host}:${pg.port}`,
        dbUrl: pg.url,
      });
      try {
        const schema = {
          tables: {
            ai_e2e_posts: {
              validator: { type: "object", properties: {}, required: [], additionalProperties: true },
              indexes: [{ name: "by_author", fields: ["author"] }],
              searchIndexes: [],
              vectorIndexes: [],
            },
          },
        };
        const code = withSchemaBundle(schema, `{
          kind: "mutation",
          args: { parse: (a) => a },
          handler: async (ctx, _args) => {
            const c = ctx.db.collection("ai_e2e_posts");
            await c.insertMany([
              { author: "u1", title: "one" },
              { author: "u2", title: "two" },
              { author: "u1", title: "three" },
            ]);
            const docs = await ctx.db.query("ai_e2e_posts")
              .filter((q) => q.eq(q.field("author"), "u1"))
              .collect();
            return docs.map((d) => d.title).sort();
          },
        }`);
        await rt.deploy("ai-e2e", code);
        const res = await rt.invoke("ai-e2e", { args: {} });
        const parsed = JSON.parse(res.body);
        assertEquals(parsed.data, ["one", "three"]);
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
  name: "E2E: /metrics exposes excalibase_query_index_selected_total after auto-selection",
  async fn() {
    const pg = await startPostgres();
    try {
      await createCollection(pg.url, "ai_metrics_posts");
      const rt = await startRuntime({
        v2Enabled: true,
        allowedHosts: `${pg.host}:${pg.port}`,
        dbUrl: pg.url,
      });
      try {
        const schema = {
          tables: {
            ai_metrics_posts: {
              validator: { type: "object", properties: {}, required: [], additionalProperties: true },
              indexes: [{ name: "by_author", fields: ["author"] }],
              searchIndexes: [],
              vectorIndexes: [],
            },
          },
        };
        const code = withSchemaBundle(schema, `{
          kind: "mutation",
          args: { parse: (a) => a },
          handler: async (ctx, _args) => {
            const c = ctx.db.collection("ai_metrics_posts");
            await c.insert({ author: "u1", title: "x" });
            await ctx.db.query("ai_metrics_posts")
              .filter((q) => q.eq(q.field("author"), "u1"))
              .collect();
            return "ok";
          },
        }`);
        await rt.deploy("ai-metrics", code);
        await rt.invoke("ai-metrics", { args: {} });
        const metricsRes = await rt.raw("/metrics", {
          method: "GET",
          headers: { "X-Runtime-Secret": rt.secret },
        });
        const text = await metricsRes.text();
        // Body should declare the counter and the auto=true label line.
        if (!text.includes("excalibase_query_index_selected_total")) {
          throw new Error("metric missing from /metrics output: " + text);
        }
        if (!text.includes(`auto="true"`)) {
          throw new Error('expected auto="true" label in /metrics: ' + text);
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
  name: "E2E: explicit withIndex does NOT bump the auto-selected counter",
  async fn() {
    const pg = await startPostgres();
    try {
      await createCollection(pg.url, "ai_explicit_posts");
      const rt = await startRuntime({
        v2Enabled: true,
        allowedHosts: `${pg.host}:${pg.port}`,
        dbUrl: pg.url,
      });
      try {
        const schema = {
          tables: {
            ai_explicit_posts: {
              validator: { type: "object", properties: {}, required: [], additionalProperties: true },
              indexes: [{ name: "by_author", fields: ["author"] }],
              searchIndexes: [],
              vectorIndexes: [],
            },
          },
        };
        const code = withSchemaBundle(schema, `{
          kind: "mutation",
          args: { parse: (a) => a },
          handler: async (ctx, _args) => {
            const c = ctx.db.collection("ai_explicit_posts");
            await c.insert({ author: "u1", title: "x" });
            // User picks the index manually — auto path must skip.
            await ctx.db.query("ai_explicit_posts")
              .withIndex("by_author", (q) => q.eq("author", "u1"))
              .collect();
            return "ok";
          },
        }`);
        await rt.deploy("ai-explicit", code);
        await rt.invoke("ai-explicit", { args: {} });
        const metricsRes = await rt.raw("/metrics", {
          method: "GET",
          headers: { "X-Runtime-Secret": rt.secret },
        });
        const text = await metricsRes.text();
        // The counter line for auto="true" should remain at 0 (or be absent).
        // We assert no `auto="true"` line with a non-zero count was emitted.
        const matchAutoTrue = text.match(/excalibase_query_index_selected_total\{[^}]*auto="true"[^}]*\}\s+(\d+)/);
        if (matchAutoTrue && Number(matchAutoTrue[1]) > 0) {
          throw new Error(`auto=true counter should be 0 for explicit withIndex; got ${matchAutoTrue[1]}`);
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
