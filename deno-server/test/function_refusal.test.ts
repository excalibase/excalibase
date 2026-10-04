// EXC-518: a function refuses a caller with a status (FunctionError); only a
// crash is a 500, and a crash's message stays in the function's logs.

import { assert, assertEquals } from "https://deno.land/std@0.224.0/assert/mod.ts";
import { startRuntime } from "./harness.ts";

function bundleDefault(expr: string): string {
  return `globalThis.__excalibase_default = ${expr};`;
}

// What `throw new FunctionError(status, message)` puts on the wire: the
// worker only sees the error's name and status, never the lib's class.
function refusal(status: number, message: string): string {
  return `(() => { const e = new Error(${JSON.stringify(message)}); e.name = "FunctionError"; e.status = ${status}; return e; })()`;
}

function v2Throwing(kind: string, thrown: string): string {
  return bundleDefault(`{
    kind: "${kind}",
    args: { parse: (a) => a },
    handler: async () => { throw ${thrown}; },
  }`);
}

async function logLines(rt: { raw: (p: string, i?: RequestInit) => Promise<Response>; secret: string }, id: string): Promise<string> {
  const res = await rt.raw(`/logs/${id}`, { headers: { "X-Runtime-Secret": rt.secret } });
  const json = await res.json() as { logs: Array<{ msg: string }> };
  return json.logs.map((l) => l.msg).join("\n");
}

const opts = { sanitizeOps: false, sanitizeResources: false };

Deno.test({
  name: "a refused caller gets the refusal's status and message",
  async fn() {
    const rt = await startRuntime({ v2Enabled: true });
    try {
      const cases: Array<[string, number, string]> = [
        ["mutation", 403, "only staff may upload product images"],
        ["query", 401, "sign in first"],
        ["action", 400, "a product image is at most 5 MB"],
      ];
      for (const [kind, status, message] of cases) {
        const id = `refuse-${status}`;
        assertEquals((await rt.deploy(id, v2Throwing(kind, refusal(status, message)))).status, 201);
        const res = await rt.invoke(id, { args: {} });
        assertEquals(res.status, status, res.body);
        assertEquals(JSON.parse(res.body), { error: message });
      }
    } finally {
      await rt.stop();
    }
  },
  ...opts,
});

Deno.test({
  name: "a crash is a 500 that tells the caller nothing; the function's logs keep the message",
  async fn() {
    const rt = await startRuntime({ v2Enabled: true });
    try {
      await rt.deploy("crash", v2Throwing("mutation", `new Error("relation secret_table does not exist")`));
      const res = await rt.invoke("crash", { args: {} });
      assertEquals(res.status, 500);
      assertEquals(JSON.parse(res.body), { error: "internal error" });
      assert((await logLines(rt, "crash")).includes("relation secret_table does not exist"));
    } finally {
      await rt.stop();
    }
  },
  ...opts,
});

Deno.test({
  name: "a FunctionError whose status is not a client error is a crash, not a refusal",
  async fn() {
    const rt = await startRuntime({ v2Enabled: true });
    try {
      await rt.deploy("bad-status", v2Throwing("mutation", refusal(503, "db host 10.0.0.7 down")));
      const res = await rt.invoke("bad-status", { args: {} });
      assertEquals(res.status, 500);
      assertEquals(JSON.parse(res.body), { error: "internal error" });
    } finally {
      await rt.stop();
    }
  },
  ...opts,
});

Deno.test({
  name: "an httpAction refuses with a status too, and its crash is a bare 500",
  async fn() {
    const rt = await startRuntime({ v2Enabled: true });
    try {
      const http = (thrown: string) => bundleDefault(`{
        kind: "httpAction", __metadata: {},
        handler: async () => { throw ${thrown}; },
      }`);
      await rt.deploy("http-refuse", http(refusal(403, "staff only")));
      const refused = await rt.invoke("http-refuse", "");
      assertEquals(refused.status, 403);
      assertEquals(JSON.parse(refused.body), { error: "staff only" });

      await rt.deploy("http-crash", http(`new Error("token=abc123")`));
      const crashed = await rt.invoke("http-crash", "");
      assertEquals(crashed.status, 500);
      assertEquals(JSON.parse(crashed.body), { error: "internal error" });
    } finally {
      await rt.stop();
    }
  },
  ...opts,
});

Deno.test({
  name: "a refusal from a nested ctx.runMutation reaches the caller with its status",
  async fn() {
    const rt = await startRuntime({ v2Enabled: true });
    try {
      await rt.deploy("proj_r__guard", v2Throwing("mutation", refusal(403, "staff only")));
      await rt.deploy("proj_r__outer", bundleDefault(`{
        kind: "mutation",
        args: { parse: (a) => a },
        handler: async (ctx) => ctx.runMutation({ moduleName: "guard", exportName: "default" }, {}),
      }`));
      const res = await rt.invoke("proj_r__outer", { args: {} });
      assertEquals(res.status, 403, res.body);
      assertEquals(JSON.parse(res.body), { error: "staff only" });
    } finally {
      await rt.stop();
    }
  },
  ...opts,
});
