// Filter expression → SQL compiler unit tests.
//
// Phase 6 introduces the Convex-shape query builder; its `filter()`
// expressions carry an AST of comparisons + and/or/not nodes. The runtime
// compiles them into a single `postgres.js` tagged template. These tests
// pin the truth tables for the logical combinators against a real Postgres
// container so we exercise the actual fragments postgres.js emits — no
// string-equality assertion can stand in for that.

import { assertEquals } from "https://deno.land/std@0.224.0/assert/mod.ts";
import { executeQueryPlan } from "../runtime/db.ts";
import type { QueryPlan } from "../runtime/db.ts";
import { createCollection, startPostgres } from "./pg_harness.ts";

async function withSeededCollection<T>(
  collection: string,
  docs: ReadonlyArray<Record<string, unknown>>,
  fn: (sql: unknown) => Promise<T>,
): Promise<T> {
  const pg = await startPostgres();
  try {
    await createCollection(pg.url, collection);
    const postgres = (await import("npm:postgres@3.4.4")).default;
    const sql = postgres(pg.url, { onnotice: () => {} });
    try {
      // Seed via tagged template so jsonb binds properly. (sql.unsafe with
      // a JSON string would land the doc as a JSON-encoded string, not an
      // object, breaking `doc->>'field'` lookups.)
      // deno-lint-ignore no-explicit-any
      const sqlAny: any = sql;
      const table = sqlAny.unsafe(`nosql."${collection}"`);
      for (const d of docs) {
        const _id = `id-${Math.random().toString(36).slice(2, 12)}`;
        const _ct = Date.now() + Math.random() * 1000;
        await sqlAny`
          INSERT INTO ${table} (_id, _creation_time, doc)
          VALUES (${_id}, ${_ct}, ${sqlAny.json(d)})
        `;
      }
      return await fn(sqlAny);
    } finally {
      await sql.end({ timeout: 1 });
    }
  } finally {
    await pg.stop();
  }
}

Deno.test({
  name: "filter eq compiles to (doc->>'field') = value and matches the right rows",
  async fn() {
    await withSeededCollection(
      "ops_eq",
      [
        { name: "alice", votes: 5 },
        { name: "bob",   votes: 10 },
        { name: "carol", votes: 5 },
      ],
      async (sql) => {
        const plan: QueryPlan = {
          collection: "ops_eq",
          filter: { kind: "eq", left: { kind: "field", name: "votes" }, right: 5 },
        };
        const out = (await executeQueryPlan(sql, plan, "collect")) as unknown as ReadonlyArray<Record<string, unknown>>;
        assertEquals(out.length, 2);
        const names = out.map((d) => d.name).sort();
        assertEquals(names, ["alice", "carol"]);
      },
    );
  },
  sanitizeOps: false,
  sanitizeResources: false,
});

Deno.test({
  name: "filter and(eq, gt) compiles into both predicates",
  async fn() {
    await withSeededCollection(
      "ops_and",
      [
        { kind: "post", votes: 1 },
        { kind: "post", votes: 7 },
        { kind: "todo", votes: 7 },
      ],
      async (sql) => {
        const plan: QueryPlan = {
          collection: "ops_and",
          filter: {
            kind: "and",
            args: [
              { kind: "eq", left: { kind: "field", name: "kind" }, right: "post" },
              { kind: "gt", left: { kind: "field", name: "votes" }, right: 5 },
            ],
          },
        };
        const out = (await executeQueryPlan(sql, plan, "collect")) as unknown as ReadonlyArray<Record<string, unknown>>;
        assertEquals(out.length, 1);
        assertEquals(out[0].kind, "post");
        assertEquals(out[0].votes, 7);
      },
    );
  },
  sanitizeOps: false,
  sanitizeResources: false,
});

Deno.test({
  name: "filter or(eq, eq) matches either branch",
  async fn() {
    await withSeededCollection(
      "ops_or",
      [
        { name: "a" },
        { name: "b" },
        { name: "c" },
      ],
      async (sql) => {
        const plan: QueryPlan = {
          collection: "ops_or",
          filter: {
            kind: "or",
            args: [
              { kind: "eq", left: { kind: "field", name: "name" }, right: "a" },
              { kind: "eq", left: { kind: "field", name: "name" }, right: "c" },
            ],
          },
        };
        const out = (await executeQueryPlan(sql, plan, "collect")) as unknown as ReadonlyArray<Record<string, unknown>>;
        assertEquals(out.length, 2);
        const names = out.map((d) => d.name).sort();
        assertEquals(names, ["a", "c"]);
      },
    );
  },
  sanitizeOps: false,
  sanitizeResources: false,
});

Deno.test({
  name: "filter not(gt) negates the predicate",
  async fn() {
    await withSeededCollection(
      "ops_not",
      [
        { votes: 0 },
        { votes: 5 },
        { votes: 10 },
      ],
      async (sql) => {
        const plan: QueryPlan = {
          collection: "ops_not",
          filter: {
            kind: "not",
            arg: { kind: "gt", left: { kind: "field", name: "votes" }, right: 5 },
          },
        };
        const out = (await executeQueryPlan(sql, plan, "collect")) as unknown as ReadonlyArray<Record<string, unknown>>;
        assertEquals(out.length, 2);
      },
    );
  },
  sanitizeOps: false,
  sanitizeResources: false,
});

Deno.test({
  name: "filter neq excludes the matching value",
  async fn() {
    await withSeededCollection(
      "ops_neq",
      [{ name: "x" }, { name: "y" }, { name: "x" }],
      async (sql) => {
        const plan: QueryPlan = {
          collection: "ops_neq",
          filter: { kind: "neq", left: { kind: "field", name: "name" }, right: "x" },
        };
        const out = (await executeQueryPlan(sql, plan, "collect")) as unknown as ReadonlyArray<Record<string, unknown>>;
        assertEquals(out.length, 1);
        assertEquals(out[0].name, "y");
      },
    );
  },
  sanitizeOps: false,
  sanitizeResources: false,
});

Deno.test({
  name: "filter gte/lte bracket numeric ranges",
  async fn() {
    await withSeededCollection(
      "ops_range",
      [{ votes: 1 }, { votes: 5 }, { votes: 10 }, { votes: 20 }],
      async (sql) => {
        const plan: QueryPlan = {
          collection: "ops_range",
          filter: {
            kind: "and",
            args: [
              { kind: "gte", left: { kind: "field", name: "votes" }, right: 5 },
              { kind: "lte", left: { kind: "field", name: "votes" }, right: 15 },
            ],
          },
        };
        const out = (await executeQueryPlan(sql, plan, "collect")) as unknown as ReadonlyArray<Record<string, unknown>>;
        const sortedVotes = out.map((d) => d.votes).sort((a, b) => Number(a) - Number(b));
        assertEquals(sortedVotes, [5, 10]);
      },
    );
  },
  sanitizeOps: false,
  sanitizeResources: false,
});

Deno.test({
  name: "filter rejects unknown logical kinds",
  async fn() {
    await withSeededCollection("ops_bad", [{ a: 1 }], async (sql) => {
      // deno-lint-ignore no-explicit-any
      const plan: any = {
        collection: "ops_bad",
        filter: { kind: "xor", args: [] },
      };
      let threw = false;
      try {
        await executeQueryPlan(sql, plan, "collect");
      } catch (e) {
        threw = true;
        if (!String((e as Error).message ?? e).toLowerCase().includes("filter")) {
          throw new Error("error should mention filter: " + (e as Error).message);
        }
      }
      assertEquals(threw, true);
    });
  },
  sanitizeOps: false,
  sanitizeResources: false,
});

Deno.test({
  name: "filter field name regex-validated; rejects injection-y identifiers",
  async fn() {
    await withSeededCollection("ops_inj", [{ a: 1 }], async (sql) => {
      const plan: QueryPlan = {
        collection: "ops_inj",
        filter: {
          kind: "eq",
          left: { kind: "field", name: "a; DROP TABLE x" },
          right: 1,
        },
      };
      let threw = false;
      try {
        await executeQueryPlan(sql, plan, "collect");
      } catch (_e) {
        threw = true;
      }
      assertEquals(threw, true);
    });
  },
  sanitizeOps: false,
  sanitizeResources: false,
});
