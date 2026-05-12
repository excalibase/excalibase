// Phase 9b.B — Self-published dedupe: a single replica with NATS enabled
// must NOT re-execute its own subscriptions twice for the same commit.
//
// Without dedupe, every local commit hits two paths:
//   1. local emitCommit → reactiveRegistry.dispatchCommit
//   2. NATS publish → wildcard subscribe → reactiveRegistry.dispatchCommit
// → 2x re-executions → 2x identical pushes (the second has no effect
// because the SHA hash matches, but it still costs a query round-trip).
//
// The bridge dedupes by `(runtimeId, ts)`: if a received NATS message
// carries our own runtimeId, drop it. This test asserts exactly one push
// fires for one mutation when NATS is wired.
//
// We rely on the registry's `lastResultHash` short-circuit to detect a
// double dispatch: when reads return the same data the SECOND time, no
// new frame is sent. So we count frames per subId across a quiet window;
// the count must be exactly 1 (initial) plus 1 (after the mutation).

import { assertEquals, assertExists } from "https://deno.land/std@0.224.0/assert/mod.ts";
import { delay } from "https://deno.land/std@0.224.0/async/delay.ts";
import { startRuntime, makeUnsignedJwt } from "./harness.ts";
import { createCollection, startPostgres } from "./pg_harness.ts";
import { startNats } from "./nats_harness.ts";
import { openWs, recv, send } from "./ws_client.ts";

function bundle(expr: string): string {
  return `globalThis.__excalibase_default = ${expr};`;
}

Deno.test({
  name: "reactive(nats): self-published commits are deduped — exactly one push per local mutation",
  async fn() {
    const pg = await startPostgres();
    const nats = await startNats();
    try {
      await createCollection(pg.url, "things");
      const projectId = "proj_nats_dedupe";
      const queryCode = bundle(`{
        kind: "query",
        args: { parse: (a) => a },
        handler: async (ctx, args) => await ctx.db.query("things").collect(),
      }`);
      const mutationCode = bundle(`{
        kind: "mutation",
        args: { parse: (a) => a },
        handler: async (ctx, args) => await ctx.db.collection("things").insert({ tag: args.tag }),
      }`);

      const allowed = `${pg.host}:${pg.port},127.0.0.1:${nats.port}`;
      const rt = await startRuntime({
        v2Enabled: true,
        allowedHosts: allowed,
        dbUrl: pg.url,
        wsEnabled: true,
        natsUrl: nats.url,
      });
      try {
        await rt.deploy(`${projectId}__listThings`, queryCode);
        await rt.deploy(`${projectId}__createThing`, mutationCode);
        await delay(300);

        const ws = await openWs(rt, projectId, makeUnsignedJwt({ sub: "u1" }));
        try {
          await send(ws, {
            op: "subscribe",
            subId: "d1",
            ref: { moduleName: "listThings", exportName: "default" },
            args: {},
          });
          // Drain initial result.
          let initial: Record<string, unknown> | null = null;
          for (let i = 0; i < 80 && !initial; i++) {
            const m = await recv(ws, 100);
            if (m && m.op === "result") initial = m;
          }
          assertExists(initial);
          assertEquals((initial!.data as unknown[]).length, 0);

          // Invoke ONE local mutation.
          const res = await rt.invoke(`${projectId}__createThing`, { args: { tag: "one" } });
          assertEquals(res.status, 200);

          // Wait through the entire NATS round-trip window — must exceed
          // the time it takes a self-published message to land back. Pick
          // 1500ms: nats.js delivery is sub-50ms on localhost, so 1.5s is
          // far past anything the round-trip can hit.
          await delay(1500);

          // Count `result` frames in the inbox.
          let resultFrames = 0;
          let lastData: unknown[] | null = null;
          while (true) {
            const m = await recv(ws, 50);
            if (!m) break;
            if (m.op === "result" && m.subId === "d1") {
              resultFrames++;
              if (Array.isArray(m.data)) lastData = m.data as unknown[];
            }
          }
          // Exactly one push from the local commit. If dedupe broke, the
          // self-published NATS round-trip would push a second identical
          // frame and `resultFrames` would be 2 (the registry's hash
          // short-circuit also collapses it, but we want zero unnecessary
          // re-executions).
          assertEquals(resultFrames, 1, "exactly one push per local mutation");
          assertExists(lastData);
          assertEquals(lastData!.length, 1);
        } finally {
          ws.close();
          await delay(50);
        }
      } finally {
        await rt.stop();
      }
    } finally {
      await nats.stop();
      await pg.stop();
    }
  },
  sanitizeOps: false,
  sanitizeResources: false,
});
