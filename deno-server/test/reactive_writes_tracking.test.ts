// Phase 9b.A — Write tracking: only INSERT/UPDATE/DELETE collections appear
// in the emitted CommitEvent's deps. Reads (find/getById/query/etc.) MUST NOT.
//
// We assert by indirection: a subscription on collection "audit" must NOT
// fire when a mutation only READS audit (then writes "log"). The mutation
// reads "audit" via getById then inserts into "log".

import { assertEquals, assertExists } from "https://deno.land/std@0.224.0/assert/mod.ts";
import { delay } from "https://deno.land/std@0.224.0/async/delay.ts";
import { startRuntime, makeUnsignedJwt } from "./harness.ts";
import { createCollection, startPostgres } from "./pg_harness.ts";
import { openWs, recv, send } from "./ws_client.ts";

function bundle(expr: string): string {
  return `globalThis.__excalibase_default = ${expr};`;
}

Deno.test({
  name: "reactive: reads do not appear in CommitEvent.deps (only writes do)",
  async fn() {
    const pg = await startPostgres();
    try {
      await createCollection(pg.url, "audit");
      await createCollection(pg.url, "log");
      const rt = await startRuntime({
        v2Enabled: true,
        allowedHosts: `${pg.host}:${pg.port}`,
        dbUrl: pg.url,
        wsEnabled: true,
      });
      try {
        const projectId = "proj_writes";
        await rt.deploy(`${projectId}__listAudit`, bundle(`{
          kind: "query",
          args: { parse: (a) => a },
          handler: async (ctx, args) => await ctx.db.query("audit").collect(),
        }`));
        // Read audit, then write to log only. audit MUST NOT appear in deps.
        await rt.deploy(`${projectId}__doMixed`, bundle(`{
          kind: "mutation",
          args: { parse: (a) => a },
          handler: async (ctx, args) => {
            await ctx.db.query("audit").collect();
            await ctx.db.collection("audit").find({});
            return await ctx.db.collection("log").insert({ event: "x" });
          },
        }`));

        const ws = await openWs(rt, projectId, makeUnsignedJwt({ sub: "u1" }));
        try {
          await send(ws, {
            op: "subscribe",
            subId: "a1",
            ref: { moduleName: "listAudit", exportName: "default" },
            args: {},
          });
          // Drain initial result.
          let initial: Record<string, unknown> | null = null;
          for (let i = 0; i < 50 && !initial; i++) {
            const m = await recv(ws, 100);
            if (m && m.op === "result") initial = m;
          }
          assertExists(initial);

          // Run the mutation; reads audit, writes log. No push expected on the
          // audit subscription.
          const res = await rt.invoke(`${projectId}__doMixed`, { args: {} });
          assertEquals(res.status, 200);
          await delay(1500);

          let saw = 0;
          while (true) {
            const m = await recv(ws, 50);
            if (!m) break;
            if (m.op === "result") saw++;
          }
          assertEquals(saw, 0, "audit subscription must not fire on writes to log");

          // Also expose a debug endpoint that returns the last CommitEvent so
          // we can directly assert its deps shape.
          const r = await fetch(`${rt.baseUrl}/reactive/debug/last-commit`, {
            headers: { "X-Runtime-Secret": rt.secret },
          });
          const j = await r.json() as { deps?: string[] };
          assertExists(j.deps);
          assertEquals(j.deps?.includes("log"), true);
          assertEquals(j.deps?.includes("audit"), false);
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
