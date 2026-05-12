// Phase 9b.A — Dependency isolation between subscriptions and mutations.
//
// Two collections, posts and comments. A subscription on query("posts").list
// must NOT re-execute when a mutation writes to comments, but MUST re-execute
// when a mutation writes to posts.

import { assertEquals, assertExists } from "https://deno.land/std@0.224.0/assert/mod.ts";
import { delay } from "https://deno.land/std@0.224.0/async/delay.ts";
import { startRuntime, makeUnsignedJwt } from "./harness.ts";
import { createCollection, startPostgres } from "./pg_harness.ts";
import { openWs, recv, send } from "./ws_client.ts";

function bundle(expr: string): string {
  return `globalThis.__excalibase_default = ${expr};`;
}

Deno.test({
  name: "reactive: mutation to unrelated collection does NOT push; mutation to watched collection does",
  async fn() {
    const pg = await startPostgres();
    try {
      await createCollection(pg.url, "posts");
      await createCollection(pg.url, "comments");
      const rt = await startRuntime({
        v2Enabled: true,
        allowedHosts: `${pg.host}:${pg.port}`,
        dbUrl: pg.url,
        wsEnabled: true,
      });
      try {
        const projectId = "proj_iso";
        const queryCode = bundle(`{
          kind: "query",
          args: { parse: (a) => a },
          handler: async (ctx, args) => await ctx.db.query("posts").collect(),
        }`);
        const postMutCode = bundle(`{
          kind: "mutation",
          args: { parse: (a) => a },
          handler: async (ctx, args) => await ctx.db.collection("posts").insert({ title: args.title || "p" }),
        }`);
        const commentMutCode = bundle(`{
          kind: "mutation",
          args: { parse: (a) => a },
          handler: async (ctx, args) => await ctx.db.collection("comments").insert({ body: args.body || "c" }),
        }`);
        await rt.deploy(`${projectId}__listPosts`, queryCode);
        await rt.deploy(`${projectId}__createPost`, postMutCode);
        await rt.deploy(`${projectId}__createComment`, commentMutCode);

        const ws = await openWs(rt, projectId, makeUnsignedJwt({ sub: "u1" }));
        try {
          await send(ws, {
            op: "subscribe",
            subId: "s1",
            ref: { moduleName: "listPosts", exportName: "default" },
            args: {},
          });
          // Drain initial result.
          let initial: Record<string, unknown> | null = null;
          for (let i = 0; i < 50 && !initial; i++) {
            const m = await recv(ws, 100);
            if (m && m.op === "result") initial = m;
          }
          assertExists(initial);

          // Mutation on UNRELATED collection — no push expected.
          const r1 = await rt.invoke(`${projectId}__createComment`, { args: { body: "hi" } });
          assertEquals(r1.status, 200);
          await delay(1500);
          // Inbox should not contain any new result frames.
          let saw = 0;
          while (true) {
            const m = await recv(ws, 50);
            if (!m) break;
            if (m.op === "result") saw++;
          }
          assertEquals(saw, 0, "no push expected for unrelated collection mutation");

          // Mutation on WATCHED collection — push expected.
          const r2 = await rt.invoke(`${projectId}__createPost`, { args: { title: "hi" } });
          assertEquals(r2.status, 200);
          const deadline = Date.now() + 2000;
          let pushed: Record<string, unknown> | null = null;
          while (!pushed && Date.now() < deadline) {
            const m = await recv(ws, 200);
            if (m && m.op === "result") pushed = m;
          }
          assertExists(pushed, "push expected after mutation on watched collection");
          const data = (pushed as { data: unknown }).data as Array<{ title: string }>;
          assertEquals(Array.isArray(data), true);
          assertEquals(data.length, 1);
          assertEquals(data[0].title, "hi");
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
