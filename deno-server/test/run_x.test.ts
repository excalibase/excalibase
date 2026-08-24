// Phase 7 — ctx.runQuery / ctx.runMutation / ctx.runAction tests.
//
// The runtime injects a runX surface on every Ctx variant (Convex parity).
// Each call posts a {type:'runX', op, ref, args} message to the main thread;
// the main thread resolves the target to either an in-process worker
// (same runtime, different fnId) or to a sibling runtime via the gateway's
// internal-invoke route. Either way, the response carries `{ok, data|error}`
// and the caller's await chain proceeds.
//
// Tests pre-deploy two functions in the same runtime so the in-process path
// is exercised (cheapest case, also Convex-equivalent semantics for
// in-project composition). Cross-runtime hops are covered by the Go tests.

import { assertEquals, assertStringIncludes } from "https://deno.land/std@0.224.0/assert/mod.ts";
import { startRuntime } from "./harness.ts";

function bundleDefault(expr: string): string {
  return `globalThis.__excalibase_default = ${expr};`;
}

Deno.test({
  name: "ctx.runQuery resolves to a sibling function in the same project",
  async fn() {
    const rt = await startRuntime({ v2Enabled: true });
    try {
      // Target — a plain query the caller will invoke via ctx.runQuery.
      const targetCode = bundleDefault(`{
        kind: "query",
        args: { parse: (a) => a },
        handler: async (_ctx, args) => ({ doubled: args.x * 2 }),
      }`);
      await rt.deploy("proj_a__double", targetCode);

      // Caller — a mutation that runs the query and returns its data.
      const callerCode = bundleDefault(`{
        kind: "mutation",
        args: { parse: (a) => a },
        handler: async (ctx, args) => {
          const inner = await ctx.runQuery(
            { moduleName: "double", exportName: "default" },
            { x: args.n }
          );
          return { outer: inner.doubled + 1 };
        },
      }`);
      await rt.deploy("proj_a__caller", callerCode);

      const res = await rt.invoke("proj_a__caller", { args: { n: 5 } });
      assertEquals(res.status, 200);
      const parsed = JSON.parse(res.body) as { data: { outer: number } };
      assertEquals(parsed.data.outer, 11); // 5*2 + 1
    } finally {
      await rt.stop();
    }
  },
  sanitizeOps: false,
  sanitizeResources: false,
});

Deno.test({
  name: "ctx.runMutation propagates handler errors back to the caller",
  async fn() {
    const rt = await startRuntime({ v2Enabled: true });
    try {
      const targetCode = bundleDefault(`{
        kind: "mutation",
        args: { parse: (a) => a },
        handler: async (_ctx, _args) => { throw new Error("nope-inner"); },
      }`);
      await rt.deploy("proj_b__broken", targetCode);
      const callerCode = bundleDefault(`{
        kind: "mutation",
        args: { parse: (a) => a },
        handler: async (ctx, _args) => {
          try {
            await ctx.runMutation(
              { moduleName: "broken", exportName: "default" }, {}
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
      assertStringIncludes(parsed.data.message, "nope-inner");
    } finally {
      await rt.stop();
    }
  },
  sanitizeOps: false,
  sanitizeResources: false,
});

Deno.test({
  name: "ctx.runQuery: depth limit prevents infinite recursion",
  async fn() {
    // Set the depth limit low so the test is fast.
    const rt = await startRuntime({ v2Enabled: true });
    try {
      // A single self-referential function that calls itself indefinitely.
      // EXCALIBASE_RUN_MAX_DEPTH default is 8 (per @excalibase/server@0.6.0
      // CHANGELOG); we don't override here so the env-default path is tested.
      const recurCode = bundleDefault(`{
        kind: "query",
        args: { parse: (a) => a },
        handler: async (ctx, args) => {
          const depth = args.depth || 0;
          if (depth > 50) return { stopped: depth };
          return await ctx.runQuery(
            { moduleName: "recur", exportName: "default" },
            { depth: depth + 1 }
          );
        },
      }`);
      await rt.deploy("proj_c__recur", recurCode);

      const res = await rt.invoke("proj_c__recur", { args: { depth: 0 } });
      // Either 500 or a 200 with an error body — accept both shapes but
      // assert the depth-limit phrase is somewhere in the response.
      const blob = res.body;
      assertStringIncludes(blob, "depth");
    } finally {
      await rt.stop();
    }
  },
  sanitizeOps: false,
  sanitizeResources: false,
});

Deno.test({
  name: "ctx.runMutation: ValidationError from target surfaces with issues array",
  async fn() {
    const rt = await startRuntime({ v2Enabled: true });
    try {
      // The target's handler does its own validation and throws a
      // ValidationError-shaped error. Phase 7 surfaces .name and .issues
      // through the runX RPC envelope so the caller sees the same shape
      // it would from a direct ctx.db ValidationError.
      const targetCode = bundleDefault(`{
        kind: "mutation",
        args: { parse: (a) => a },
        handler: async (_ctx, args) => {
          if (typeof args.n !== "number") {
            const e = new Error("expected number");
            e.name = "ValidationError";
            e.issues = [{ path: "n", message: "expected number" }];
            throw e;
          }
          return { n: args.n };
        },
      }`);
      await rt.deploy("proj_d__strict", targetCode);
      const callerCode = bundleDefault(`{
        kind: "mutation",
        args: { parse: (a) => a },
        handler: async (ctx, _args) => {
          try {
            await ctx.runMutation(
              { moduleName: "strict", exportName: "default" },
              { n: "not-a-number" }
            );
            return { caught: false };
          } catch (err) {
            return {
              caught: true,
              name: err.name || "",
              issues: err.issues || [],
            };
          }
        },
      }`);
      await rt.deploy("proj_d__caller", callerCode);
      const res = await rt.invoke("proj_d__caller", { args: {} });
      assertEquals(res.status, 200);
      const parsed = JSON.parse(res.body) as {
        data: { caught: boolean; name: string; issues: Array<{ path: string }> };
      };
      assertEquals(parsed.data.caught, true);
      assertEquals(parsed.data.name, "ValidationError");
      assertEquals(parsed.data.issues.length, 1);
      assertEquals(parsed.data.issues[0].path, "n");
    } finally {
      await rt.stop();
    }
  },
  sanitizeOps: false,
  sanitizeResources: false,
});
