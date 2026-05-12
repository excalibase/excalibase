// Phase 9b.A — Single-replica reactive query: end-to-end push.
//
// Test plan:
//   1. Deploy two functions in the same project: a query("posts").collect()
//      and a mutation("posts").insert(...).
//   2. Open a WebSocket to /functions/v1/{projectId}/_watch?token=<jwt>.
//   3. Send {op:"subscribe", subId:"1", ref:{moduleName, exportName}, args:{}}.
//   4. Assert the initial {op:"result", subId:"1", data:[]} arrives.
//   5. Invoke the mutation via HTTP to insert one document.
//   6. Assert a follow-up {op:"result", subId:"1", data:[{...inserted}]} arrives
//      within 2 seconds.
//
// All transports are real: real Postgres in a container, real worker, real WS.

import { assertEquals, assertExists } from "https://deno.land/std@0.224.0/assert/mod.ts";
import { delay } from "https://deno.land/std@0.224.0/async/delay.ts";
import { startRuntime, makeUnsignedJwt } from "./harness.ts";
import { createCollection, startPostgres } from "./pg_harness.ts";
import { openWs, recv, send, type WsClient } from "./ws_client.ts";

function bundle(expr: string): string {
  return `globalThis.__excalibase_default = ${expr};`;
}

// Wait up to `timeoutMs` for a message matching `match`. Returns the matching
// message or throws on timeout. Other messages are queued back for later recv.
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
  name: "reactive: subscribe receives initial result and follow-up after a mutation commits",
  async fn() {
    const pg = await startPostgres();
    try {
      await createCollection(pg.url, "posts");
      const rt = await startRuntime({
        v2Enabled: true,
        allowedHosts: `${pg.host}:${pg.port}`,
        dbUrl: pg.url,
        wsEnabled: true,
      });
      try {
        const projectId = "proj_reactive";
        const queryCode = bundle(`{
          kind: "query",
          args: { parse: (a) => a },
          handler: async (ctx, args) => {
            return await ctx.db.query("posts").collect();
          },
        }`);
        const mutationCode = bundle(`{
          kind: "mutation",
          args: { parse: (a) => a },
          handler: async (ctx, args) => {
            return await ctx.db.collection("posts").insert({ title: args.title });
          },
        }`);
        await rt.deploy(`${projectId}__listPosts`, queryCode);
        await rt.deploy(`${projectId}__createPost`, mutationCode);

        const ws = await openWs(rt, projectId, makeUnsignedJwt({ sub: "u1" }));
        try {
          await send(ws, {
            op: "subscribe",
            subId: "1",
            ref: { moduleName: "listPosts", exportName: "default" },
            args: {},
          });
          const first = await recvMatching(ws, (m) => m.op === "result" && m.subId === "1");
          assertEquals(first.op, "result");
          assertEquals(first.subId, "1");
          assertEquals(first.data, []);

          // Trigger a mutation via the runtime's invoke endpoint.
          const res = await rt.invoke(`${projectId}__createPost`, {
            args: { title: "hello" },
          });
          assertEquals(res.status, 200);

          // Now the push should arrive within 2s.
          const second = await recvMatching(ws, (m) =>
            m.op === "result" && m.subId === "1" && Array.isArray(m.data) && (m.data as unknown[]).length === 1
          );
          assertEquals(second.op, "result");
          const data = second.data as Array<{ title: string; _id: string; _creationTime: number }>;
          assertEquals(data.length, 1);
          assertEquals(data[0].title, "hello");
          assertExists(data[0]._id);
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
