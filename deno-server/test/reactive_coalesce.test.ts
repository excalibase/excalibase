// Phase 9b.A — Coalescing burst of commits.
//
// Fire 10 mutations on the watched collection back-to-back. The
// subscription must receive: at least one update beyond the initial
// result; at most 10 pushes (we never re-execute more times than commits).
// The final state on the wire matches the database.

import { assertEquals } from "https://deno.land/std@0.224.0/assert/mod.ts";
import { delay } from "https://deno.land/std@0.224.0/async/delay.ts";
import { startRuntime, makeUnsignedJwt } from "./harness.ts";
import { createCollection, startPostgres } from "./pg_harness.ts";
import { openWs, recv, send } from "./ws_client.ts";

function bundle(expr: string): string {
  return `globalThis.__excalibase_default = ${expr};`;
}

Deno.test({
  name: "reactive: coalesces a burst of mutations into ≥1 and ≤N pushes",
  async fn() {
    const pg = await startPostgres();
    try {
      await createCollection(pg.url, "items");
      const rt = await startRuntime({
        v2Enabled: true,
        allowedHosts: `${pg.host}:${pg.port}`,
        dbUrl: pg.url,
        wsEnabled: true,
      });
      try {
        const projectId = "proj_coal";
        await rt.deploy(`${projectId}__list`, bundle(`{
          kind: "query",
          args: { parse: (a) => a },
          handler: async (ctx, args) => await ctx.db.query("items").collect(),
        }`));
        await rt.deploy(`${projectId}__create`, bundle(`{
          kind: "mutation",
          args: { parse: (a) => a },
          handler: async (ctx, args) => await ctx.db.collection("items").insert({ n: args.n }),
        }`));

        const ws = await openWs(rt, projectId, makeUnsignedJwt({ sub: "u1" }));
        try {
          await send(ws, {
            op: "subscribe",
            subId: "c1",
            ref: { moduleName: "list", exportName: "default" },
            args: {},
          });
          // Drain initial.
          for (let i = 0; i < 50; i++) {
            const m = await recv(ws, 100);
            if (m && m.op === "result") break;
          }

          // Fire 10 mutations back-to-back (sequential, no awaiting pushes in
          // between — we want to test coalescing).
          const N = 10;
          for (let i = 0; i < N; i++) {
            await rt.invoke(`${projectId}__create`, { args: { n: i } });
          }

          // Drain pushes for up to 5s.
          const updates: Array<Record<string, unknown>> = [];
          const deadline = Date.now() + 5000;
          let lastSeen = Date.now();
          while (Date.now() < deadline) {
            const m = await recv(ws, 200);
            if (m && m.op === "result") {
              updates.push(m);
              lastSeen = Date.now();
            } else if (Date.now() - lastSeen > 800 && updates.length > 0) {
              // No new messages for 800ms — assume coalescing settled.
              break;
            }
          }
          if (updates.length < 1) {
            throw new Error("expected at least one push beyond initial");
          }
          if (updates.length > N) {
            throw new Error(`expected at most ${N} pushes, got ${updates.length}`);
          }
          const final = updates[updates.length - 1].data as Array<{ n: number }>;
          // Final state must reflect all 10 inserts.
          assertEquals(final.length, N);
          await delay(50);
        } finally {
          ws.close();
          await delay(50);
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
