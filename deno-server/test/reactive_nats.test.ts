// Phase 9b.B — Cross-replica reactive: subscribers on runtime A see commits
// from runtime B (and vice-versa). Two Deno runtime subprocesses share one
// Postgres + one NATS container; each runs the NATS bridge in main thread.
//
// Test plan:
//   1. Start one Postgres container and one NATS container.
//   2. Boot runtime A (port pa, ws port wpa) with EXCALIBASE_NATS_URL set.
//   3. Boot runtime B (port pb, ws port wpb) with the same NATS URL.
//   4. Deploy the same query + mutation bundle to BOTH runtimes (same id).
//   5. Open a WS subscription on runtime A.
//   6. POST the mutation to runtime B → expect WS push on A within 2s.
//   7. Open a second WS subscription on runtime B; POST mutation to A →
//      expect WS push on B within 2s.
//
// If the publisher path is broken: step 6 hangs (no push on A). If the
// subscriber path is broken on A: step 6 hangs. Both directions are
// asserted independently so we know publisher + subscriber both work.

import { assertEquals, assertExists } from "https://deno.land/std@0.224.0/assert/mod.ts";
import { delay } from "https://deno.land/std@0.224.0/async/delay.ts";
import { startRuntime, makeUnsignedJwt } from "./harness.ts";
import { createCollection, startPostgres } from "./pg_harness.ts";
import { startNats } from "./nats_harness.ts";
import { openWs, recv, send, type WsClient } from "./ws_client.ts";

function bundle(expr: string): string {
  return `globalThis.__excalibase_default = ${expr};`;
}

async function recvMatching(
  client: WsClient,
  match: (m: Record<string, unknown>) => boolean,
  timeoutMs = 2000,
): Promise<Record<string, unknown>> {
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    const remaining = Math.max(50, deadline - Date.now());
    const msg = await recv(client, remaining);
    if (msg && match(msg)) return msg;
  }
  throw new Error(`no message matched within ${timeoutMs}ms`);
}

Deno.test({
  name: "reactive(nats): subscriber on runtime A receives push from mutation on runtime B",
  async fn() {
    const pg = await startPostgres();
    const nats = await startNats();
    try {
      await createCollection(pg.url, "posts");
      const projectId = "proj_nats_xreplica";
      const queryCode = bundle(`{
        kind: "query",
        args: { parse: (a) => a },
        handler: async (ctx, args) => await ctx.db.query("posts").collect(),
      }`);
      const mutationCode = bundle(`{
        kind: "mutation",
        args: { parse: (a) => a },
        handler: async (ctx, args) => await ctx.db.collection("posts").insert({ title: args.title }),
      }`);

      // Allow-list: both Postgres and NATS host:port for the main process.
      const allowed = `${pg.host}:${pg.port},127.0.0.1:${nats.port}`;
      const rtA = await startRuntime({
        v2Enabled: true,
        allowedHosts: allowed,
        dbUrl: pg.url,
        wsEnabled: true,
        natsUrl: nats.url,
      });
      try {
        const rtB = await startRuntime({
          v2Enabled: true,
          allowedHosts: allowed,
          dbUrl: pg.url,
          wsEnabled: true,
          natsUrl: nats.url,
        });
        try {
          await rtA.deploy(`${projectId}__listPosts`, queryCode);
          await rtA.deploy(`${projectId}__createPost`, mutationCode);
          await rtB.deploy(`${projectId}__listPosts`, queryCode);
          await rtB.deploy(`${projectId}__createPost`, mutationCode);
          // Give bridges a beat to connect + subscribe to wildcard.
          await delay(300);

          // -----------------------------------------------------------------
          // Direction 1: subscribe on A, mutate on B → push on A.
          // -----------------------------------------------------------------
          const wsA = await openWs(rtA, projectId, makeUnsignedJwt({ sub: "u1" }));
          try {
            await send(wsA, {
              op: "subscribe",
              subId: "1",
              ref: { moduleName: "listPosts", exportName: "default" },
              args: {},
            });
            const first = await recvMatching(wsA, (m) =>
              m.op === "result" && m.subId === "1");
            assertEquals(first.data, []);

            const res = await rtB.invoke(`${projectId}__createPost`, {
              args: { title: "hello-from-B" },
            });
            assertEquals(res.status, 200);

            const second = await recvMatching(wsA, (m) =>
              m.op === "result" && m.subId === "1"
              && Array.isArray(m.data) && (m.data as unknown[]).length === 1,
              4000,
            );
            const data = second.data as Array<{ title: string; _id: string }>;
            assertEquals(data.length, 1);
            assertEquals(data[0].title, "hello-from-B");
            assertExists(data[0]._id);
          } finally {
            wsA.close();
            await delay(50);
          }

          // -----------------------------------------------------------------
          // Direction 2: subscribe on B, mutate on A → push on B.
          // -----------------------------------------------------------------
          const wsB = await openWs(rtB, projectId, makeUnsignedJwt({ sub: "u2" }));
          try {
            await send(wsB, {
              op: "subscribe",
              subId: "2",
              ref: { moduleName: "listPosts", exportName: "default" },
              args: {},
            });
            // The collection now has the row from direction 1; initial result
            // returns length 1.
            const first = await recvMatching(wsB, (m) =>
              m.op === "result" && m.subId === "2");
            const firstData = first.data as Array<{ title: string }>;
            assertEquals(firstData.length, 1);

            const res = await rtA.invoke(`${projectId}__createPost`, {
              args: { title: "hello-from-A" },
            });
            assertEquals(res.status, 200);

            const second = await recvMatching(wsB, (m) =>
              m.op === "result" && m.subId === "2"
              && Array.isArray(m.data) && (m.data as unknown[]).length === 2,
              4000,
            );
            const data = second.data as Array<{ title: string }>;
            assertEquals(data.length, 2);
            const titles = data.map((d) => d.title).sort();
            assertEquals(titles, ["hello-from-A", "hello-from-B"]);
          } finally {
            wsB.close();
            await delay(50);
          }
        } finally {
          await rtB.stop();
        }
      } finally {
        await rtA.stop();
      }
    } finally {
      await nats.stop();
      await pg.stop();
    }
  },
  sanitizeOps: false,
  sanitizeResources: false,
});
