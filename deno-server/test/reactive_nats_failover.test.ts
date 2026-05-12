// Phase 9b.B — Failover: when NATS dies, cross-replica updates stop;
// local subscriptions on the publishing replica still fire. After NATS
// comes back, the bridge reconnects (auto-reconnect from npm:nats) and
// cross-replica events resume.
//
// Test plan:
//   1. Start Postgres + NATS, boot runtime A + runtime B with the bridge.
//   2. Subscribe on A; mutate on B; assert push on A (sanity — works).
//   3. `docker kill` the NATS container.
//   4. Mutate on B; wait 3s; assert NO push arrives on A (cross-replica
//      is currently down).
//   5. Subscribe on B (local sub on the publishing replica) and mutate B
//      again; assert the LOCAL push fires (local path unaffected).
//   6. Restart NATS on the SAME host port; wait ~3s for the bridge to
//      reconnect.
//   7. Mutate on B once more; assert push on A arrives (cross-replica
//      back).
//
// We restart NATS by stopping the original container and launching a new
// one on the same host port using `docker run`. The NATS client's
// auto-reconnect should pick it up within its 1s base / 30s cap window.

import { assertEquals, assertExists } from "https://deno.land/std@0.224.0/assert/mod.ts";
import { delay } from "https://deno.land/std@0.224.0/async/delay.ts";
import { startRuntime, makeUnsignedJwt } from "./harness.ts";
import { createCollection, startPostgres } from "./pg_harness.ts";
import { startNats, type NatsHandle } from "./nats_harness.ts";
import { openWs, recv, send, type WsClient } from "./ws_client.ts";

function bundle(expr: string): string {
  return `globalThis.__excalibase_default = ${expr};`;
}

async function recvMatching(
  client: WsClient,
  match: (m: Record<string, unknown>) => boolean,
  timeoutMs = 4000,
): Promise<Record<string, unknown> | null> {
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    const remaining = Math.max(50, deadline - Date.now());
    const msg = await recv(client, remaining);
    if (msg && match(msg)) return msg;
  }
  return null;
}

async function tcpReady(host: string, port: number): Promise<boolean> {
  try {
    const conn = await Deno.connect({ hostname: host, port });
    conn.close();
    return true;
  } catch (_) {
    return false;
  }
}

async function restartNatsOnPort(port: number): Promise<NatsHandle> {
  const name = `excalibase-natstest-${crypto.randomUUID().slice(0, 8)}`;
  const run = new Deno.Command("docker", {
    args: ["run", "-d", "--rm", "--name", name, "-p", `${port}:4222`, "nats:2-alpine"],
    stdout: "piped",
    stderr: "piped",
  });
  const { code, stdout, stderr } = await run.output();
  if (code !== 0) {
    throw new Error(`docker run nats failed: ${new TextDecoder().decode(stderr)}`);
  }
  const containerId = new TextDecoder().decode(stdout).trim();
  const deadline = Date.now() + 30_000;
  while (Date.now() < deadline) {
    if (await tcpReady("127.0.0.1", port)) break;
    await delay(100);
  }
  return {
    url: `nats://127.0.0.1:${port}`,
    port,
    containerId,
    containerName: name,
    stop: async () => {
      try {
        await new Deno.Command("docker", {
          args: ["kill", name],
          stdout: "null",
          stderr: "null",
        }).output();
      } catch (_) { /* ignore */ }
    },
  };
}

Deno.test({
  name: "reactive(nats): cross-replica push survives NATS outage + reconnect",
  async fn() {
    const pg = await startPostgres();
    let nats: NatsHandle | null = await startNats();
    let restarted: NatsHandle | null = null;
    try {
      await createCollection(pg.url, "items");
      const projectId = "proj_nats_failover";
      const queryCode = bundle(`{
        kind: "query",
        args: { parse: (a) => a },
        handler: async (ctx, args) => await ctx.db.query("items").collect(),
      }`);
      const mutationCode = bundle(`{
        kind: "mutation",
        args: { parse: (a) => a },
        handler: async (ctx, args) => await ctx.db.collection("items").insert({ name: args.name }),
      }`);

      const allowed = `${pg.host}:${pg.port},127.0.0.1:${nats.port}`;
      const rtA = await startRuntime({
        v2Enabled: true, allowedHosts: allowed, dbUrl: pg.url,
        wsEnabled: true, natsUrl: nats.url,
      });
      try {
        const rtB = await startRuntime({
          v2Enabled: true, allowedHosts: allowed, dbUrl: pg.url,
          wsEnabled: true, natsUrl: nats.url,
        });
        try {
          await rtA.deploy(`${projectId}__listItems`, queryCode);
          await rtA.deploy(`${projectId}__createItem`, mutationCode);
          await rtB.deploy(`${projectId}__listItems`, queryCode);
          await rtB.deploy(`${projectId}__createItem`, mutationCode);
          await delay(300);

          // --- Sanity: cross-replica works pre-outage ----------------------
          const wsA = await openWs(rtA, projectId, makeUnsignedJwt({ sub: "uA" }));
          try {
            await send(wsA, {
              op: "subscribe",
              subId: "x1",
              ref: { moduleName: "listItems", exportName: "default" },
              args: {},
            });
            const firstA = await recvMatching(wsA, (m) =>
              m.op === "result" && m.subId === "x1");
            assertExists(firstA);
            const r1 = await rtB.invoke(`${projectId}__createItem`, { args: { name: "pre" } });
            assertEquals(r1.status, 200);
            const pushPre = await recvMatching(wsA, (m) =>
              m.op === "result" && m.subId === "x1"
              && Array.isArray(m.data) && (m.data as unknown[]).length === 1,
              4000,
            );
            assertExists(pushPre);

            // --- Kill NATS ------------------------------------------------
            await nats!.stop();
            // Drain any pending messages, then wait until both runtimes
            // observe the disconnect. 2s is generous given nats.js's default
            // pingInterval.
            while (true) {
              const m = await recv(wsA, 100);
              if (!m) break;
            }
            await delay(2000);

            // --- Cross-replica mutation while NATS is down ----------------
            const r2 = await rtB.invoke(`${projectId}__createItem`, { args: { name: "outage" } });
            assertEquals(r2.status, 200);
            const phantom = await recvMatching(wsA, (m) =>
              m.op === "result" && m.subId === "x1"
              && Array.isArray(m.data) && (m.data as unknown[]).length === 2,
              3000,
            );
            assertEquals(phantom, null, "no push expected on A while NATS down");

            // --- Local subscription on B still works ----------------------
            const wsB = await openWs(rtB, projectId, makeUnsignedJwt({ sub: "uB" }));
            try {
              await send(wsB, {
                op: "subscribe",
                subId: "y1",
                ref: { moduleName: "listItems", exportName: "default" },
                args: {},
              });
              const firstB = await recvMatching(wsB, (m) =>
                m.op === "result" && m.subId === "y1");
              const firstBData = firstB!.data as unknown[];
              assertEquals(firstBData.length, 2);
              const r3 = await rtB.invoke(`${projectId}__createItem`, { args: { name: "localB" } });
              assertEquals(r3.status, 200);
              const localPush = await recvMatching(wsB, (m) =>
                m.op === "result" && m.subId === "y1"
                && Array.isArray(m.data) && (m.data as unknown[]).length === 3,
                4000,
              );
              assertExists(localPush, "local subscription on B must still fire while NATS is down");
            } finally {
              wsB.close();
              await delay(50);
            }

            // --- Restart NATS on the same port -----------------------------
            restarted = await restartNatsOnPort(nats!.port);
            // nats.js auto-reconnects on a backoff. Give it generous slack.
            await delay(5000);

            // --- Cross-replica works again ---------------------------------
            const r4 = await rtB.invoke(`${projectId}__createItem`, { args: { name: "after" } });
            assertEquals(r4.status, 200);
            const pushAfter = await recvMatching(wsA, (m) =>
              m.op === "result" && m.subId === "x1"
              && Array.isArray(m.data) && (m.data as unknown[]).length >= 4,
              10000,
            );
            assertExists(pushAfter, "cross-replica push must resume after NATS reconnect");
          } finally {
            wsA.close();
            await delay(50);
          }
        } finally {
          await rtB.stop();
        }
      } finally {
        await rtA.stop();
      }
    } finally {
      if (restarted) await restarted.stop();
      else if (nats) await nats.stop();
      await pg.stop();
    }
  },
  sanitizeOps: false,
  sanitizeResources: false,
});
