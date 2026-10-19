// EXC-640: a function must `export default (req: Request) => Response`.
// Anything else is refused at deploy with a message naming the contract, and
// the refusal and any handler crash are readable from /logs/{id}.

import { assertEquals, assertStringIncludes } from "https://deno.land/std@0.224.0/assert/mod.ts";
import { startRuntime } from "./harness.ts";

const opts = { sanitizeOps: false, sanitizeResources: false };

async function logText(rt: { raw: (p: string, i?: RequestInit) => Promise<Response>; secret: string }, id: string) {
  const res = await rt.raw(`/logs/${id}`, { headers: { "X-Runtime-Secret": rt.secret } });
  assertEquals(res.status, 200);
  const body = await res.json() as { logs: { level: string; msg: string }[] };
  return body.logs.map((l) => `${l.level}: ${l.msg}`).join("\n");
}

Deno.test({
  name: "a named export without a default is refused at deploy with 400 naming the contract",
  async fn() {
    const rt = await startRuntime({ v2Enabled: true });
    try {
      const res = await rt.deploy("named", `export function invite(req) { return new Response("x"); }`);
      assertEquals(res.status, 400);
      const { error } = await res.json() as { error: string };
      assertStringIncludes(error, "export default");
      assertStringIncludes(error, "invite");
      assertStringIncludes(await logText(rt, "named"), "export default");
    } finally {
      await rt.stop();
    }
  },
  ...opts,
});

Deno.test({
  name: "a default export that is not a handler is refused at deploy",
  async fn() {
    const rt = await startRuntime({ v2Enabled: true });
    try {
      const res = await rt.deploy("obj", `export default { greet: () => "hi" };`);
      assertEquals(res.status, 400);
      assertStringIncludes((await res.json() as { error: string }).error, "export default");
    } finally {
      await rt.stop();
    }
  },
  ...opts,
});

Deno.test({
  name: "a refused redeploy keeps the deployed version answering and logs the refusal",
  async fn() {
    const rt = await startRuntime({ v2Enabled: true });
    try {
      assertEquals((await rt.deploy("keep", `export default () => new Response("v1");`)).status, 201);
      assertEquals((await rt.deploy("keep", `export const nope = 1;`)).status, 400);
      const res = await rt.invoke("keep", {});
      assertEquals([res.status, res.body], [200, "v1"]);
      assertStringIncludes(await logText(rt, "keep"), "export default");
    } finally {
      await rt.stop();
    }
  },
  ...opts,
});

Deno.test({
  name: "a handler that throws answers internal error and its message is in the logs",
  async fn() {
    const rt = await startRuntime({ v2Enabled: true });
    try {
      assertEquals((await rt.deploy("boom", `export default () => { throw new Error("db password wrong"); };`)).status, 201);
      const res = await rt.invoke("boom", {});
      assertEquals(res.status, 500);
      assertEquals(JSON.parse(res.body), { error: "internal error" });
      assertStringIncludes(await logText(rt, "boom"), "db password wrong");
    } finally {
      await rt.stop();
    }
  },
  ...opts,
});
