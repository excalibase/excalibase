// Phase 6.5 — EXPLAIN-based verification that the Postgres planner actually
// picks the auto-selected index.
//
// Strategy: build a real B-tree index on the JSONB expression we WHERE
// against, seed enough rows for the planner to prefer the index over a
// seq scan, run executeQueryPlan with the auto-applied index hint, and
// parse `EXPLAIN (FORMAT JSON)` of an equivalent statement to assert
// the chosen plan node references our index.
//
// Why an equivalent statement and not the real one? `executeQueryPlan`
// runs the SELECT and discards the EXPLAIN. Cleanest path: reproduce
// the WHERE clause inline against the same connection, then EXPLAIN
// the projection.

import { assertEquals } from "https://deno.land/std@0.224.0/assert/mod.ts";
import { selectIndex } from "../runtime/index_selector.ts";
import type { QueryPlan, FilterExpr } from "../runtime/db.ts";
import type { IndexDef } from "../runtime/schema.ts";
import { executeQueryPlan } from "../runtime/db.ts";
import { startPostgres, createCollection } from "./pg_harness.ts";

function eq(field: string, value: unknown): FilterExpr {
  return { kind: "eq", left: { kind: "field", name: field }, right: value };
}

// Use a generous threshold so the planner picks the index over a seq scan
// even on a tiny test table.
async function seedAuthors(pgUrl: string, table: string, n: number): Promise<void> {
  const postgres = (await import("npm:postgres@3.4.4")).default;
  const sql = postgres(pgUrl, { onnotice: () => {} });
  try {
    // deno-lint-ignore no-explicit-any
    const sqlAny: any = sql;
    const docs: Array<{ _id: string; _creation_time: number; doc: unknown }> = [];
    for (let i = 0; i < n; i++) {
      docs.push({
        _id: `r-${i}`,
        _creation_time: i,
        doc: sqlAny.json({ author: `u${i % 10}`, n: i }),
      });
    }
    const quoted = sqlAny.unsafe(`nosql."${table}"`);
    await sqlAny`
      INSERT INTO ${quoted} ${sqlAny(docs, "_id", "_creation_time", "doc")}
    `;
  } finally {
    await sql.end({ timeout: 1 });
  }
}

interface ExplainNode {
  "Node Type"?: string;
  "Index Name"?: string;
  "Plans"?: ExplainNode[];
}

function findIndexNames(node: ExplainNode | undefined, acc: string[]): void {
  if (!node) return;
  if (typeof node["Index Name"] === "string") acc.push(node["Index Name"]);
  if (Array.isArray(node.Plans)) {
    for (const child of node.Plans) findIndexNames(child, acc);
  }
}

Deno.test({
  name: "EXPLAIN: single-field auto-selected index produces an Index Scan referencing the b-tree",
  async fn() {
    const pg = await startPostgres();
    try {
      await createCollection(pg.url, "ai_explain_posts");
      const postgres = (await import("npm:postgres@3.4.4")).default;
      const sql = postgres(pg.url, { onnotice: () => {} });
      try {
        // Build a real B-tree on the JSONB author field.
        await sql.unsafe(`
          CREATE INDEX ai_by_author ON nosql."ai_explain_posts" ((doc->>'author'))
        `);
        await seedAuthors(pg.url, "ai_explain_posts", 500);
        await sql.unsafe(`ANALYZE nosql."ai_explain_posts"`);

        const basePlan: QueryPlan = {
          collection: "ai_explain_posts",
          filter: eq("author", "u3"),
        };
        const indexes: readonly IndexDef[] = [
          { name: "ai_by_author", fields: ["author"] },
        ];
        const sel = selectIndex(basePlan, indexes);
        if (sel === null) throw new Error("expected auto-selection");
        const finalPlan: QueryPlan = { ...basePlan, index: sel, autoSelectedIndex: sel.name };

        // Sanity: the run returns rows.
        // deno-lint-ignore no-explicit-any
        const rows = (await executeQueryPlan(sql as any, finalPlan, "collect")) as ReadonlyArray<Record<string, unknown>>;
        if (rows.length === 0) throw new Error("expected rows");

        // Now EXPLAIN an equivalent statement to inspect the planner's choice.
        const explained = await sql.unsafe(
          `EXPLAIN (FORMAT JSON)
             SELECT _id, _creation_time, doc
             FROM nosql."ai_explain_posts"
             WHERE (doc->>'author') = 'u3'
             ORDER BY _creation_time ASC, _id ASC`,
        );
        // deno-lint-ignore no-explicit-any
        const rawPlan = (explained[0] as any)["QUERY PLAN"];
        const planJson = Array.isArray(rawPlan) ? rawPlan[0] : rawPlan;
        const indexNames: string[] = [];
        findIndexNames(planJson?.Plan, indexNames);
        if (!indexNames.includes("ai_by_author")) {
          throw new Error(
            "expected planner to pick ai_by_author; saw: " + JSON.stringify(indexNames),
          );
        }
        assertEquals(indexNames.includes("ai_by_author"), true);
      } finally {
        await sql.end({ timeout: 1 });
      }
    } finally {
      await pg.stop();
    }
  },
  sanitizeOps: false,
  sanitizeResources: false,
});
