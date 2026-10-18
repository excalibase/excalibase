// EXC-625 — ctx.db under the permissions the runtime image really starts
// with. The rest of the suite grants all of the environment, which hid that
// postgres.js reads PG* variables the image does not allow: every ctx.db call
// failed in production while these tests passed.

import { assertEquals } from "https://deno.land/std@0.224.0/assert/mod.ts";
import { startRuntime } from "./harness.ts";
import { createCollection, startPostgres } from "./pg_harness.ts";

Deno.test({
  name: "exc-625: ctx.db writes and reads under the image's env permissions",
  async fn() {
    const pg = await startPostgres();
    try {
      await createCollection(pg.url, "notes");
      const rt = await startRuntime({ v2Enabled: true, dbUrl: pg.url, imageEnvPermissions: true });
      try {
        await rt.deploy("proj_perm__add", `globalThis.__excalibase_default = {
          kind: "mutation", args: { parse: (a) => a },
          handler: async (ctx, args) => ctx.db.collection("notes").insert({ body: args.body }),
        };`);
        await rt.deploy("proj_perm__list", `globalThis.__excalibase_default = {
          kind: "query", args: { parse: (a) => a },
          handler: async (ctx) => (await ctx.db.collection("notes").find()).map((n) => n.body),
        };`);

        const added = await rt.invoke("proj_perm__add", { args: { body: "first" } });
        assertEquals(added.status, 200, `body=${added.body}`);
        const listed = await rt.invoke("proj_perm__list", { args: {} });
        assertEquals(listed.status, 200, `body=${listed.body}`);
        assertEquals(JSON.parse(listed.body).data, ["first"]);
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
