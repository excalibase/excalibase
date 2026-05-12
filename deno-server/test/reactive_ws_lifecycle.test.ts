// Phase 9b.A — WS lifecycle: subscriptions are cleaned up on disconnect.
//
// After the client closes the socket, the SubscriptionRegistry must remove
// every entry it held for that conn. Subsequent mutations must NOT attempt
// to push to the dead conn (and must not log errors).

import { assertEquals } from "https://deno.land/std@0.224.0/assert/mod.ts";
import { delay } from "https://deno.land/std@0.224.0/async/delay.ts";
import { startRuntime, makeUnsignedJwt } from "./harness.ts";
import { createCollection, startPostgres } from "./pg_harness.ts";
import { openWs, recv, send } from "./ws_client.ts";

function bundle(expr: string): string {
  return `globalThis.__excalibase_default = ${expr};`;
}

Deno.test({
  name: "reactive: closing the websocket cleans up all subscriptions for that conn",
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
        const projectId = "proj_life";
        await rt.deploy(`${projectId}__list`, bundle(`{
          kind: "query",
          args: { parse: (a) => a },
          handler: async (ctx, args) => await ctx.db.query("items").collect(),
        }`));
        await rt.deploy(`${projectId}__create`, bundle(`{
          kind: "mutation",
          args: { parse: (a) => a },
          handler: async (ctx, args) => await ctx.db.collection("items").insert({ n: args.n || 1 }),
        }`));

        const ws = await openWs(rt, projectId, makeUnsignedJwt({ sub: "u1" }));
        await send(ws, {
          op: "subscribe",
          subId: "x1",
          ref: { moduleName: "list", exportName: "default" },
          args: {},
        });
        // Drain initial result.
        for (let i = 0; i < 50; i++) {
          const m = await recv(ws, 100);
          if (m && m.op === "result") break;
        }

        // Sub count via debug endpoint should be 1 now.
        const before = await fetch(`${rt.baseUrl}/reactive/debug`, {
          headers: { "X-Runtime-Secret": rt.secret },
        });
        const beforeJson = await before.json() as { subscriptions: number };
        assertEquals(beforeJson.subscriptions, 1);

        // Close. Wait until the runtime observes the close.
        ws.close();
        let after = beforeJson;
        for (let i = 0; i < 50; i++) {
          await delay(100);
          const r = await fetch(`${rt.baseUrl}/reactive/debug`, {
            headers: { "X-Runtime-Secret": rt.secret },
          });
          after = await r.json() as { subscriptions: number };
          if (after.subscriptions === 0) break;
        }
        assertEquals(after.subscriptions, 0, "all subscriptions should be cleaned up");

        // Trigger a mutation — must not throw, no leaked errors.
        const res = await rt.invoke(`${projectId}__create`, { args: { n: 1 } });
        assertEquals(res.status, 200);
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
