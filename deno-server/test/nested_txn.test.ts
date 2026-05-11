// Phase 8 — nested-mutation shared transaction tests.
//
// When a mutation handler calls `ctx.runMutation(api.x.y, args)` the inner
// mutation must run inside the SAME Postgres transaction as the outer
// mutation. Convex parity: the composite write is atomic — outer rollback
// rolls inner back, and vice-versa.
//
// Mutation -> action -> mutation: actions have no transactional boundary,
// so the inner mutation (called from an action) opens its OWN transaction
// independent of the outer mutation's. We assert that path too.

import { assertEquals } from "https://deno.land/std@0.224.0/assert/mod.ts";
import { startRuntime } from "./harness.ts";
import { createCollection, startPostgres } from "./pg_harness.ts";

function bundleDefault(expr: string): string {
  return `globalThis.__excalibase_default = ${expr};`;
}

Deno.test({
  name: "nested ctx.runMutation shares the outer mutation's Postgres txn",
  async fn() {
    const pg = await startPostgres();
    try {
      await createCollection(pg.url, "audit");
      const rt = await startRuntime({ v2Enabled: true, dbUrl: pg.url });
      try {
        // Inner mutation — writes one audit row, then returns the inserted id.
        const innerCode = bundleDefault(`{
          kind: "mutation",
          args: { parse: (a) => a },
          handler: async (ctx, args) => {
            const doc = await ctx.db.collection("audit").insert({ tag: args.tag });
            return { id: doc._id };
          },
        }`);
        await rt.deploy("proj_txn__inner", innerCode);

        // Outer mutation — calls inner, then THROWS. The inner insert must
        // roll back together with the outer.
        const outerCode = bundleDefault(`{
          kind: "mutation",
          args: { parse: (a) => a },
          handler: async (ctx, _args) => {
            await ctx.runMutation(
              { moduleName: "inner", exportName: "default" },
              { tag: "rollback-marker" }
            );
            throw new Error("outer-fail");
          },
        }`);
        await rt.deploy("proj_txn__outer", outerCode);

        await rt.invoke("proj_txn__outer", { args: {} });

        // Assert the inner write rolled back.
        const postgres = (await import("npm:postgres@3.4.4")).default;
        const sql = postgres(pg.url, { onnotice: () => {} });
        try {
          const rows = await sql.unsafe(
            `SELECT _id FROM nosql.audit WHERE doc->>'tag' = $1`,
            ["rollback-marker"],
          ) as unknown as Array<Record<string, unknown>>;
          assertEquals(rows.length, 0, "inner write must roll back with outer");
        } finally {
          await sql.end({ timeout: 1 });
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
  name: "successful nested mutation commits inner+outer together",
  async fn() {
    const pg = await startPostgres();
    try {
      await createCollection(pg.url, "audit");
      const rt = await startRuntime({ v2Enabled: true, dbUrl: pg.url });
      try {
        const innerCode = bundleDefault(`{
          kind: "mutation",
          args: { parse: (a) => a },
          handler: async (ctx, args) => {
            const doc = await ctx.db.collection("audit").insert({ tag: args.tag, src: "inner" });
            return { id: doc._id };
          },
        }`);
        await rt.deploy("proj_txn2__inner", innerCode);

        const outerCode = bundleDefault(`{
          kind: "mutation",
          args: { parse: (a) => a },
          handler: async (ctx, _args) => {
            const inner = await ctx.runMutation(
              { moduleName: "inner", exportName: "default" },
              { tag: "commit-marker" }
            );
            const outer = await ctx.db.collection("audit").insert({ tag: "commit-marker", src: "outer" });
            return { innerId: inner.id, outerId: outer._id };
          },
        }`);
        await rt.deploy("proj_txn2__outer", outerCode);

        const res = await rt.invoke("proj_txn2__outer", { args: {} });
        assertEquals(res.status, 200);

        const postgres = (await import("npm:postgres@3.4.4")).default;
        const sql = postgres(pg.url, { onnotice: () => {} });
        try {
          const rows = await sql.unsafe(
            `SELECT _id FROM nosql.audit WHERE doc->>'tag' = $1`,
            ["commit-marker"],
          ) as unknown as Array<Record<string, unknown>>;
          assertEquals(rows.length, 2, "both inner + outer must commit");
        } finally {
          await sql.end({ timeout: 1 });
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
  name: "mutation→action→mutation: action's inner mutation opens its own txn",
  async fn() {
    const pg = await startPostgres();
    try {
      await createCollection(pg.url, "audit");
      const rt = await startRuntime({ v2Enabled: true, dbUrl: pg.url });
      try {
        // Innermost mutation — writes one row.
        const innerCode = bundleDefault(`{
          kind: "mutation",
          args: { parse: (a) => a },
          handler: async (ctx, args) => {
            const doc = await ctx.db.collection("audit").insert({ tag: args.tag, src: "innermost" });
            return { id: doc._id };
          },
        }`);
        await rt.deploy("proj_mam__inner", innerCode);

        // Middle action — invokes the inner mutation, then returns. Actions
        // can't run inside a transaction, so the inner mutation must open
        // its own.
        const midCode = bundleDefault(`{
          kind: "action",
          args: { parse: (a) => a },
          handler: async (ctx, _args) => {
            const r = await ctx.runMutation(
              { moduleName: "inner", exportName: "default" },
              { tag: "split-marker" }
            );
            return r;
          },
        }`);
        await rt.deploy("proj_mam__mid", midCode);

        // Outer mutation — calls the action, then THROWS. The action's
        // schedule-mutation commit must NOT roll back when the outer fails.
        const outerCode = bundleDefault(`{
          kind: "mutation",
          args: { parse: (a) => a },
          handler: async (ctx, _args) => {
            await ctx.runAction(
              { moduleName: "mid", exportName: "default" },
              {}
            );
            throw new Error("outer-fail");
          },
        }`);
        await rt.deploy("proj_mam__outer", outerCode);

        await rt.invoke("proj_mam__outer", { args: {} });

        const postgres = (await import("npm:postgres@3.4.4")).default;
        const sql = postgres(pg.url, { onnotice: () => {} });
        try {
          const rows = await sql.unsafe(
            `SELECT _id FROM nosql.audit WHERE doc->>'tag' = $1`,
            ["split-marker"],
          ) as unknown as Array<Record<string, unknown>>;
          assertEquals(rows.length, 1, "action's inner mutation must commit independently of outer");
        } finally {
          await sql.end({ timeout: 1 });
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
