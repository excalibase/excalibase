// EXC-560: a v2 function's zod `args` are enforced by the runtime, as the
// @excalibase/server README promises: bad args are a 400 with the issues and
// never reach the handler; good args reach it as the schema parsed them.

import { assertEquals } from "https://deno.land/std@0.224.0/assert/mod.ts";
import { startRuntime } from "./harness.ts";

// A zod-shaped schema: `word` must be a non-empty string; parsing trims it.
const WORD_SCHEMA = `{
  safeParse: (a) => (a && typeof a.word === "string" && a.word.trim().length > 0)
    ? { success: true, data: { word: a.word.trim() } }
    : { success: false, error: { issues: [{ path: ["word"], message: "word must be a non-empty string" }] } },
}`;

function echoBundle(kind: string): string {
  return `globalThis.__excalibase_default = {
    kind: "${kind}",
    args: ${WORD_SCHEMA},
    handler: async (ctx, args) => ({ got: args }),
  };`;
}

const opts = { sanitizeOps: false, sanitizeResources: false };

Deno.test({
  name: "args the schema refuses are a 400 with the issues, and the handler never runs",
  async fn() {
    const rt = await startRuntime({ v2Enabled: true });
    try {
      for (const kind of ["query", "mutation", "action"]) {
        const id = `echo-${kind}`;
        assertEquals((await rt.deploy(id, echoBundle(kind))).status, 201);
        for (const args of [{ word: "" }, { word: 7 }, {}]) {
          const res = await rt.invoke(id, { args });
          assertEquals(res.status, 400, `${kind} ${JSON.stringify(args)}: ${res.body}`);
          assertEquals(JSON.parse(res.body), {
            error: "args validation failed",
            issues: [{ path: ["word"], message: "word must be a non-empty string" }],
          });
        }
      }
    } finally {
      await rt.stop();
    }
  },
  ...opts,
});

Deno.test({
  name: "args the schema accepts reach the handler as parsed",
  async fn() {
    const rt = await startRuntime({ v2Enabled: true });
    try {
      assertEquals((await rt.deploy("echo", echoBundle("query"))).status, 201);
      const res = await rt.invoke("echo", { args: { word: "  hi  " } });
      assertEquals(res.status, 200, res.body);
      assertEquals(JSON.parse(res.body), { data: { got: { word: "hi" } } });
    } finally {
      await rt.stop();
    }
  },
  ...opts,
});

Deno.test({
  name: "a hand-written def whose args has no safeParse passes args through",
  async fn() {
    const rt = await startRuntime({ v2Enabled: true });
    try {
      const bundle = `globalThis.__excalibase_default = {
        kind: "query", args: { parse: (a) => a }, handler: async (ctx, args) => ({ got: args }),
      };`;
      assertEquals((await rt.deploy("raw", bundle)).status, 201);
      const res = await rt.invoke("raw", { args: { any: 1 } });
      assertEquals(res.status, 200, res.body);
      assertEquals(JSON.parse(res.body), { data: { got: { any: 1 } } });
    } finally {
      await rt.stop();
    }
  },
  ...opts,
});
