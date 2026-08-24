// Phase 8 — runtime read-only enforcement for ctx.runX.
//
// QueryCtx is declared read-only at the type level (no `runMutation`/
// `runAction` slot). Hand-rolled bundles can still call those methods at
// runtime, so the main thread's dispatcher must enforce the kind matrix:
//
//   query   → query      OK
//   query   → mutation   ERROR  ("Calling mutation X from a query is not allowed")
//   query   → action     ERROR
//   mutation→ query      OK
//   mutation→ mutation   OK (shared txn — see nested_txn.test.ts)
//   mutation→ action     OK (action runs independently)
//   action  → *          OK
//
// The runtime knows the caller's kind from the script table (set on deploy
// when the worker reports its export metadata). The target's kind is
// looked up the same way. A mismatch surfaces a clean error message back
// through the runX RPC, which the worker re-throws to the user handler.

import { assertEquals, assertStringIncludes } from "https://deno.land/std@0.224.0/assert/mod.ts";
import { startRuntime } from "./harness.ts";

function bundleDefault(expr: string): string {
  return `globalThis.__excalibase_default = ${expr};`;
}

Deno.test({
  name: "ctx.runMutation from a query handler is rejected at runtime",
  async fn() {
    const rt = await startRuntime({ v2Enabled: true });
    try {
      // Target — a mutation. Calling it from a query MUST be rejected.
      const targetCode = bundleDefault(`{
        kind: "mutation",
        args: { parse: (a) => a },
        handler: async (_ctx, _args) => ({ ok: true }),
      }`);
      await rt.deploy("proj_a__target", targetCode);

      // Caller — a query that tries to invoke the mutation via runMutation.
      // QueryCtx wouldn't normally have runMutation (TS-level), but the
      // worker shim attaches all three on every Ctx so a hand-rolled bundle
      // can still try. The runtime must reject before dispatching.
      const callerCode = bundleDefault(`{
        kind: "query",
        args: { parse: (a) => a },
        handler: async (ctx, _args) => {
          try {
            await ctx.runMutation(
              { moduleName: "target", exportName: "default" }, {}
            );
            return { caught: false };
          } catch (err) {
            return { caught: true, message: err.message };
          }
        },
      }`);
      await rt.deploy("proj_a__caller", callerCode);

      const res = await rt.invoke("proj_a__caller", { args: {} });
      assertEquals(res.status, 200);
      const parsed = JSON.parse(res.body) as { data: { caught: boolean; message: string } };
      assertEquals(parsed.data.caught, true);
      assertStringIncludes(parsed.data.message.toLowerCase(), "mutation");
      assertStringIncludes(parsed.data.message.toLowerCase(), "query");
    } finally {
      await rt.stop();
    }
  },
  sanitizeOps: false,
  sanitizeResources: false,
});

Deno.test({
  name: "ctx.runAction from a query handler is rejected at runtime",
  async fn() {
    const rt = await startRuntime({ v2Enabled: true });
    try {
      const targetCode = bundleDefault(`{
        kind: "action",
        args: { parse: (a) => a },
        handler: async (_ctx, _args) => ({ ok: true }),
      }`);
      await rt.deploy("proj_b__target", targetCode);

      const callerCode = bundleDefault(`{
        kind: "query",
        args: { parse: (a) => a },
        handler: async (ctx, _args) => {
          try {
            await ctx.runAction(
              { moduleName: "target", exportName: "default" }, {}
            );
            return { caught: false };
          } catch (err) {
            return { caught: true, message: err.message };
          }
        },
      }`);
      await rt.deploy("proj_b__caller", callerCode);

      const res = await rt.invoke("proj_b__caller", { args: {} });
      assertEquals(res.status, 200);
      const parsed = JSON.parse(res.body) as { data: { caught: boolean; message: string } };
      assertEquals(parsed.data.caught, true);
      assertStringIncludes(parsed.data.message.toLowerCase(), "action");
      assertStringIncludes(parsed.data.message.toLowerCase(), "query");
    } finally {
      await rt.stop();
    }
  },
  sanitizeOps: false,
  sanitizeResources: false,
});

Deno.test({
  name: "ctx.runQuery from a query handler succeeds (read-only chain ok)",
  async fn() {
    const rt = await startRuntime({ v2Enabled: true });
    try {
      const targetCode = bundleDefault(`{
        kind: "query",
        args: { parse: (a) => a },
        handler: async (_ctx, args) => ({ x: (args.x ?? 0) + 1 }),
      }`);
      await rt.deploy("proj_c__inner", targetCode);

      const callerCode = bundleDefault(`{
        kind: "query",
        args: { parse: (a) => a },
        handler: async (ctx, _args) => {
          const r = await ctx.runQuery(
            { moduleName: "inner", exportName: "default" }, { x: 41 }
          );
          return r;
        },
      }`);
      await rt.deploy("proj_c__outer", callerCode);

      const res = await rt.invoke("proj_c__outer", { args: {} });
      assertEquals(res.status, 200);
      const parsed = JSON.parse(res.body) as { data: { x: number } };
      assertEquals(parsed.data.x, 42);
    } finally {
      await rt.stop();
    }
  },
  sanitizeOps: false,
  sanitizeResources: false,
});

Deno.test({
  name: "ctx.runMutation from a mutation succeeds",
  async fn() {
    const rt = await startRuntime({ v2Enabled: true });
    try {
      const targetCode = bundleDefault(`{
        kind: "mutation",
        args: { parse: (a) => a },
        handler: async (_ctx, args) => ({ doubled: args.n * 2 }),
      }`);
      await rt.deploy("proj_d__inner", targetCode);

      const callerCode = bundleDefault(`{
        kind: "mutation",
        args: { parse: (a) => a },
        handler: async (ctx, _args) => {
          const r = await ctx.runMutation(
            { moduleName: "inner", exportName: "default" }, { n: 21 }
          );
          return r;
        },
      }`);
      await rt.deploy("proj_d__outer", callerCode);

      const res = await rt.invoke("proj_d__outer", { args: {} });
      assertEquals(res.status, 200);
      const parsed = JSON.parse(res.body) as { data: { doubled: number } };
      assertEquals(parsed.data.doubled, 42);
    } finally {
      await rt.stop();
    }
  },
  sanitizeOps: false,
  sanitizeResources: false,
});
