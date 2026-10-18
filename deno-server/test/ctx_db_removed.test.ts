// EXC-625 — ctx.db was removed with the document data plane (@excalibase/server 0.11.0).
// Every function kind sees a ctx.db that refuses use with an error naming
// what to do instead, under the permissions the runtime image really has.

import { assertEquals, assertMatch } from "https://deno.land/std@0.224.0/assert/mod.ts";
import { startRuntime } from "./harness.ts";

const REMOVED = /^ctx\.db is not available: read and write your project's data through its GraphQL or REST API/;

function probe(kind: string): string {
  return `globalThis.__excalibase_default = {
    kind: "${kind}", args: { parse: (a) => a },
    handler: async (ctx) => {
      console.log("ctx", ctx); // inspecting ctx must not throw
      try { await ctx.db.collection("notes").insert({ body: "x" }); return "no error"; }
      catch (err) { return err.message; }
    },
  };`;
}

Deno.test({
  name: "exc-625: ctx.db refuses use on every kind with a clear error",
  async fn() {
    const rt = await startRuntime({ v2Enabled: true, imageEnvPermissions: true });
    try {
      for (const kind of ["query", "mutation", "action"]) {
        await rt.deploy(`proj_rm__${kind}`, probe(kind));
        const res = await rt.invoke(`proj_rm__${kind}`, { args: {} });
        assertEquals(res.status, 200, `${kind}: body=${res.body}`);
        assertMatch(JSON.parse(res.body).data, REMOVED, kind);
      }
    } finally {
      await rt.stop();
    }
  },
  sanitizeOps: false,
  sanitizeResources: false,
});

Deno.test({
  name: "exc-625: an httpAction's ctx.db refuses use with the same error",
  async fn() {
    const rt = await startRuntime({ v2Enabled: true, imageEnvPermissions: true });
    try {
      await rt.deploy("proj_rm__http", `globalThis.__excalibase_default = {
        kind: "httpAction",
        handler: async (ctx) => {
          try { ctx.db.query("notes"); return new Response("no error"); }
          catch (err) { return new Response(err.message); }
        },
      };`);
      const res = await rt.invoke("proj_rm__http", "");
      assertEquals(res.status, 200, `body=${res.body}`);
      assertMatch(res.body, REMOVED);
    } finally {
      await rt.stop();
    }
  },
  sanitizeOps: false,
  sanitizeResources: false,
});

Deno.test({
  name: "exc-625: a mutation that uses ctx.db fails without opening anything",
  async fn() {
    const rt = await startRuntime({ v2Enabled: true, imageEnvPermissions: true });
    try {
      await rt.deploy("proj_rm__write", `globalThis.__excalibase_default = {
        kind: "mutation", args: { parse: (a) => a },
        handler: async (ctx) => ctx.db.collection("notes").insert({ body: "x" }),
      };`);
      const res = await rt.invoke("proj_rm__write", { args: {} });
      assertEquals(res.status, 500, `body=${res.body}`);
      assertEquals(JSON.parse(res.body), { error: "internal error" });
      const logs = await (await rt.raw("/logs/proj_rm__write", { headers: { "X-Runtime-Secret": rt.secret } })).json();
      assertMatch(JSON.stringify(logs), /ctx\.db is not available/);
    } finally {
      await rt.stop();
    }
  },
  sanitizeOps: false,
  sanitizeResources: false,
});
