// V2 dispatch tests — exercise the new tagged FunctionDef shape
// (kind=query|mutation|action) and the ctx injection contract.
//
// These tests spawn the real server.ts subprocess so the assertions cover
// the full worker boot + dispatch path, not a hand-rolled stub.

import { assertEquals, assertStringIncludes } from "https://deno.land/std@0.224.0/assert/mod.ts";
import { makeUnsignedJwt, startRuntime } from "./harness.ts";

// Bundle template — the runtime contract is that the deploy `code` payload
// has assigned its export to globalThis.__excalibase_default. The Go side
// produces this via esbuild + a trailer; in tests we inline the assignment.
function bundleDefault(expr: string): string {
  return `globalThis.__excalibase_default = ${expr};`;
}

// queryBody returns the canonical v2 invoke payload — { args: ... } wrapped.
function queryBody(args: unknown): string {
  return JSON.stringify({ args });
}

Deno.test({
  name: "v2 query: handler called with ctx + args, response wrapped in {data}",
  async fn() {
    // Phase 1: query ctx.db is a real DbClient. The handler only needs to
    // observe its presence — no DB call is made here.
    const rt = await startRuntime({ v2Enabled: true });
    try {
      const fnCode = bundleDefault(`{
        kind: "query",
        args: { parse: (a) => a },
        handler: async (ctx, args) => ({
          greeting: "hi " + args.name,
          // Phase 1: query ctx must expose a DbClient (object), not null.
          dbType: ctx.db === null ? "null" : typeof ctx.db,
        }),
      }`);
      const deploy = await rt.deploy("v2q", fnCode);
      assertEquals(deploy.status, 201);

      const res = await rt.invoke("v2q", { args: { name: "duc" } });
      assertEquals(res.status, 200);
      const parsed = JSON.parse(res.body);
      assertEquals(parsed.data.greeting, "hi duc");
      assertEquals(parsed.data.dbType, "object");
    } finally {
      await rt.stop();
    }
  },
  sanitizeOps: false,
  sanitizeResources: false,
});

Deno.test({
  name: "v2 action: ctx.db is null (Convex parity)",
  async fn() {
    const rt = await startRuntime({ v2Enabled: true });
    try {
      const fnCode = bundleDefault(`{
        kind: "action",
        args: { parse: (a) => a },
        handler: async (ctx, _args) => ({ dbIsNull: ctx.db === null }),
      }`);
      await rt.deploy("v2a-nodb", fnCode);
      const res = await rt.invoke("v2a-nodb", { args: {} });
      assertEquals(res.status, 200);
      const parsed = JSON.parse(res.body);
      assertEquals(parsed.data.dbIsNull, true);
    } finally {
      await rt.stop();
    }
  },
  sanitizeOps: false,
  sanitizeResources: false,
});

Deno.test({
  name: "v2 mutation: identical contract to query",
  async fn() {
    const rt = await startRuntime({ v2Enabled: true });
    try {
      const fnCode = bundleDefault(`{
        kind: "mutation",
        args: { parse: (a) => a },
        handler: async (_ctx, args) => ({ created: args.title }),
      }`);
      await rt.deploy("v2m", fnCode);
      const res = await rt.invoke("v2m", { args: { title: "post" } });
      assertEquals(res.status, 200);
      const parsed = JSON.parse(res.body);
      assertEquals(parsed.data.created, "post");
    } finally {
      await rt.stop();
    }
  },
  sanitizeOps: false,
  sanitizeResources: false,
});

Deno.test({
  name: "v2 action: identical contract to query",
  async fn() {
    const rt = await startRuntime({ v2Enabled: true });
    try {
      const fnCode = bundleDefault(`{
        kind: "action",
        args: { parse: (a) => a },
        handler: async (_ctx, args) => "ran: " + args.cmd,
      }`);
      await rt.deploy("v2a", fnCode);
      const res = await rt.invoke("v2a", { args: { cmd: "ping" } });
      assertEquals(res.status, 200);
      const parsed = JSON.parse(res.body);
      assertEquals(parsed.data, "ran: ping");
    } finally {
      await rt.stop();
    }
  },
  sanitizeOps: false,
  sanitizeResources: false,
});

Deno.test({
  name: "v2 ctx.auth.claims populated from Authorization Bearer",
  async fn() {
    const rt = await startRuntime({ v2Enabled: true });
    try {
      const fnCode = bundleDefault(`{
        kind: "query",
        args: { parse: (a) => a },
        handler: async (ctx, _args) => ({
          claims: ctx.auth.claims,
          sub: ctx.auth.claims && ctx.auth.claims.sub,
        }),
      }`);
      await rt.deploy("v2auth", fnCode);

      const jwt = makeUnsignedJwt({ sub: "user-123", role: "authenticated" });
      const res = await rt.invoke("v2auth", { args: {} }, {
        Authorization: "Bearer " + jwt,
      });
      assertEquals(res.status, 200);
      const parsed = JSON.parse(res.body);
      assertEquals(parsed.data.sub, "user-123");
      assertEquals(parsed.data.claims.role, "authenticated");
    } finally {
      await rt.stop();
    }
  },
  sanitizeOps: false,
  sanitizeResources: false,
});

Deno.test({
  name: "v2 missing Authorization header → ctx.auth.claims is null, handler still runs",
  async fn() {
    const rt = await startRuntime({ v2Enabled: true });
    try {
      const fnCode = bundleDefault(`{
        kind: "query",
        args: { parse: (a) => a },
        handler: async (ctx, _args) => ({ claims: ctx.auth.claims }),
      }`);
      await rt.deploy("v2noauth", fnCode);
      const res = await rt.invoke("v2noauth", { args: {} });
      assertEquals(res.status, 200);
      const parsed = JSON.parse(res.body);
      assertEquals(parsed.data.claims, null);
    } finally {
      await rt.stop();
    }
  },
  sanitizeOps: false,
  sanitizeResources: false,
});

Deno.test({
  name: "v2 body without 'args' field → 400",
  async fn() {
    const rt = await startRuntime({ v2Enabled: true });
    try {
      const fnCode = bundleDefault(`{
        kind: "query",
        args: { parse: (a) => a },
        handler: async (_ctx, _args) => ({ ok: true }),
      }`);
      await rt.deploy("v2bad", fnCode);
      // Send body that doesn't contain { args: ... }.
      const res = await rt.invoke("v2bad", { somethingElse: 1 });
      assertEquals(res.status, 400);
      assertStringIncludes(res.body, "args");
    } finally {
      await rt.stop();
    }
  },
  sanitizeOps: false,
  sanitizeResources: false,
});

Deno.test({
  name: "v2 handler throws → 500 with error message",
  async fn() {
    const rt = await startRuntime({ v2Enabled: true });
    try {
      const fnCode = bundleDefault(`{
        kind: "query",
        args: { parse: (a) => a },
        handler: async () => { throw new Error("boom"); },
      }`);
      await rt.deploy("v2err", fnCode);
      const res = await rt.invoke("v2err", { args: {} });
      assertEquals(res.status, 500);
      assertStringIncludes(res.body, "boom");
    } finally {
      await rt.stop();
    }
  },
  sanitizeOps: false,
  sanitizeResources: false,
});

Deno.test({
  name: "flag off: v2 shape falls back to legacy Fetch dispatch",
  async fn() {
    // EXCALIBASE_FUNCTIONS_V2 unset — runtime should ignore the shape and
    // attempt to call the export as (req)=>Response. The export is a plain
    // object, so the legacy handler will fail at typeof handler !== 'function'
    // and the response should reflect that legacy error.
    const rt = await startRuntime({ v2Enabled: false });
    try {
      const fnCode = bundleDefault(`{
        kind: "query",
        args: { parse: (a) => a },
        handler: async () => ({ ok: true }),
      }`);
      await rt.deploy("flagoff", fnCode);
      const res = await rt.invoke("flagoff", { args: {} });
      // Legacy path: the export is not a function, so worker rejects with
      // the legacy "No default export found" / "must export default" error,
      // which surfaces as a runtime error (non-200) and matches the byte-
      // identical behaviour of today's runtime.
      // Note: rt.invoke surfaces server errors as 500 with the error text.
      // Either way, it must NOT be a v2-wrapped {data:...} success body.
      if (res.status === 200 && res.body.startsWith("{\"data\":")) {
        throw new Error("flag-off should not dispatch via v2 path");
      }
    } finally {
      await rt.stop();
    }
  },
  sanitizeOps: false,
  sanitizeResources: false,
});

Deno.test({
  name: "legacy Fetch handler still works when flag is on (regression guard)",
  async fn() {
    const rt = await startRuntime({ v2Enabled: true });
    try {
      // Plain Fetch handler — should bypass v2 detection (not an object with
      // kind/args/handler) and run via the legacy path unchanged.
      const fnCode = bundleDefault(
        `async (req) => new Response(JSON.stringify({ legacy: true, method: req.method }), { headers: { "content-type": "application/json" } })`,
      );
      await rt.deploy("legacy", fnCode);
      const res = await rt.invoke("legacy", "ignored body");
      assertEquals(res.status, 200);
      const parsed = JSON.parse(res.body);
      assertEquals(parsed.legacy, true);
      assertEquals(parsed.method, "POST");
    } finally {
      await rt.stop();
    }
  },
  sanitizeOps: false,
  sanitizeResources: false,
});
